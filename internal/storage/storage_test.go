package storage_test

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/nibra/gosiptea/internal/app"
	"github.com/nibra/gosiptea/internal/storage"
)

func TestListContactsRejectsSymlinkToFIFOWithoutBlocking(t *testing.T) {
	dir := t.TempDir()
	fifo := filepath.Join(dir, "contacts.fifo")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	configDir := filepath.Join(dir, "baresip")
	if err := os.Mkdir(configDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(fifo, filepath.Join(configDir, "contacts")); err != nil {
		t.Fatal(err)
	}

	done := make(chan error, 1)
	go func() {
		_, err := storage.New(configDir).ListContacts()
		done <- err
	}()
	select {
	case err := <-done:
		if !errors.Is(err, storage.ErrNotRegular) {
			t.Fatalf("ListContacts() error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("ListContacts blocked while opening a FIFO")
	}
}

func TestAccountRoundTripModesAndPasswordRetention(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "baresip")
	store := storage.New(dir)

	if err := store.WriteAccount(storage.AccountCredentials{
		Server:   "pbx.example.com:5061",
		Username: "101",
		Password: "first-secret",
	}); err != nil {
		t.Fatalf("WriteAccount() error = %v", err)
	}

	assertMode(t, dir, 0o700)
	assertMode(t, filepath.Join(dir, "accounts"), 0o600)
	assertMode(t, filepath.Join(dir, ".oma.sip.lock"), 0o600)

	account, err := store.ReadAccount()
	if err != nil {
		t.Fatalf("ReadAccount() error = %v", err)
	}
	want := storage.Account{
		Configured:  true,
		Username:    "101",
		Domain:      "pbx.example.com:5061",
		Server:      "pbx.example.com:5061",
		Login:       "101",
		HasPassword: true,
		Secure:      true,
		Transport:   "tls",
	}
	if account != want {
		t.Fatalf("ReadAccount() = %#v, want %#v", account, want)
	}

	if err := store.WriteAccount(storage.AccountCredentials{
		Server:   "proxy.example.com",
		Username: "102",
		Domain:   "voice.example.com",
		Login:    "login-102",
	}); err != nil {
		t.Fatalf("WriteAccount() retaining password error = %v", err)
	}
	contents := readFile(t, filepath.Join(dir, "accounts"))
	for _, fragment := range []string{
		"<sip:102@voice.example.com;transport=tls>",
		"auth_user=login-102",
		"auth_pass=first-secret",
		`outbound="sip:proxy.example.com;transport=tls";mediaenc=srtp`,
	} {
		if !strings.Contains(contents, fragment) {
			t.Errorf("accounts does not contain %q:\n%s", fragment, contents)
		}
	}
	if strings.Count(contents, "auth_pass=") != 1 {
		t.Errorf("accounts has more than one account line:\n%s", contents)
	}

	insecure := false
	if err := store.WriteAccount(storage.AccountCredentials{
		Server:   "pbx.example.com",
		Username: "103",
		Password: "udp-secret",
		Secure:   &insecure,
	}); err != nil {
		t.Fatalf("WriteAccount() insecure error = %v", err)
	}
	contents = readFile(t, filepath.Join(dir, "accounts"))
	if strings.Contains(contents, "transport=tls") || strings.Contains(contents, "mediaenc=srtp") {
		t.Fatalf("insecure account contains secure transport settings:\n%s", contents)
	}
	account, err = store.ReadAccount()
	if err != nil {
		t.Fatalf("ReadAccount() insecure error = %v", err)
	}
	if account.Secure {
		t.Fatalf("ReadAccount().Secure = true, want false")
	}
	assertNoTemporaryFiles(t, dir)
}

func TestAccountRejectsUnsafeAndOversizedFields(t *testing.T) {
	store := storage.New(filepath.Join(t.TempDir(), "baresip"))
	badPasswords := []string{"space secret", "semi;secret", `quote"secret`, "line\nbreak", "angle<secret"}
	for _, password := range badPasswords {
		t.Run(fmt.Sprintf("password_%q", password), func(t *testing.T) {
			err := store.WriteAccount(storage.AccountCredentials{
				Server:   "pbx.example.com",
				Username: "101",
				Password: password,
			})
			if !errors.Is(err, storage.ErrInvalid) {
				t.Fatalf("WriteAccount() error = %v, want ErrInvalid", err)
			}
		})
	}

	err := store.WriteAccount(storage.AccountCredentials{
		Server:   strings.Repeat("a", storage.MaxFieldLength+1),
		Username: "101",
		Password: "secret",
	})
	if !errors.Is(err, storage.ErrInvalid) {
		t.Fatalf("oversized WriteAccount() error = %v, want ErrInvalid", err)
	}
}

func TestAccountWriteRefusesSymlink(t *testing.T) {
	base := t.TempDir()
	dir := filepath.Join(base, "baresip")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(base, "outside-accounts")
	if err := os.WriteFile(outside, []byte("do not replace\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "accounts")); err != nil {
		t.Fatal(err)
	}

	err := storage.New(dir).WriteAccount(storage.AccountCredentials{
		Server:   "pbx.example.com",
		Username: "101",
		Password: "secret",
	})
	if !errors.Is(err, storage.ErrSymlink) {
		t.Fatalf("WriteAccount() error = %v, want ErrSymlink", err)
	}
	if got := readFile(t, outside); got != "do not replace\n" {
		t.Fatalf("symlink target changed to %q", got)
	}
}

func TestContactsPreserveLinesRejectDuplicatesAndRemoveAllMatches(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "baresip")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "contacts")
	original := "# personal contacts\n" +
		"unparsed text stays here\n" +
		`"Alice" <sip:alice@example.com>;presence=yes` + "\n" +
		`"Alice duplicate" <sip:alice@example.com>;access=allow` + "\n" +
		`<sips:bob@example.com>` + "\n"
	if err := os.WriteFile(path, []byte(original), 0o640); err != nil {
		t.Fatal(err)
	}
	store := storage.New(dir)

	list, err := store.ListContacts()
	if err != nil {
		t.Fatalf("ListContacts() error = %v", err)
	}
	if !list.Configured || len(list.Contacts) != 3 {
		t.Fatalf("ListContacts() = %#v, want configured list of three", list)
	}
	if list.Contacts[0].Params != ";presence=yes" {
		t.Fatalf("first contact params = %q", list.Contacts[0].Params)
	}

	err = store.AddContact(storage.Contact{Name: "Another Alice", URI: "sip:alice@example.com"})
	if !errors.Is(err, storage.ErrDuplicate) {
		t.Fatalf("duplicate AddContact() error = %v, want ErrDuplicate", err)
	}
	if got := readFile(t, path); got != original {
		t.Fatalf("duplicate add changed contacts:\n%s", got)
	}

	if err := store.AddContact(storage.Contact{Name: "Carol", URI: "sip:carol@example.com"}); err != nil {
		t.Fatalf("AddContact() error = %v", err)
	}
	got := readFile(t, path)
	if !strings.HasPrefix(got, original) || !strings.HasSuffix(got, `"Carol" <sip:carol@example.com>`+"\n") {
		t.Fatalf("AddContact() did not preserve old lines:\n%s", got)
	}
	assertMode(t, dir, 0o700)
	assertMode(t, path, 0o640)

	if err := store.RemoveContact("sip:alice@example.com"); err != nil {
		t.Fatalf("RemoveContact() error = %v", err)
	}
	got = readFile(t, path)
	if strings.Contains(got, "sip:alice@example.com") {
		t.Fatalf("RemoveContact() left a duplicate:\n%s", got)
	}
	for _, preserved := range []string{"# personal contacts", "unparsed text stays here", "sips:bob@example.com", "sip:carol@example.com"} {
		if !strings.Contains(got, preserved) {
			t.Errorf("RemoveContact() lost %q:\n%s", preserved, got)
		}
	}
	assertMode(t, path, 0o640)
	assertNoTemporaryFiles(t, dir)
}

func TestRemoveContactAcceptsExactExistingURIParameters(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "baresip")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "contacts")
	if err := os.WriteFile(path, []byte("<sip:alice@example.com;transport=tls>\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := storage.New(dir).RemoveContact("sip:alice@example.com;transport=tls"); err != nil {
		t.Fatalf("RemoveContact() error = %v", err)
	}
	if got := readFile(t, path); strings.Contains(got, "alice") {
		t.Fatalf("parameterized contact was not removed: %q", got)
	}
}

func TestContactsValidateURIAndRefuseSymlinkWrites(t *testing.T) {
	store := storage.New(filepath.Join(t.TempDir(), "baresip"))
	for _, uri := range []string{"", "alice@example.com", "sip:@example.com", "sip:alice@", "sip:a@b@c", "sip:alice@example.com;transport=tls", `sip:ali"ce@example.com`} {
		err := store.AddContact(storage.Contact{Name: "Alice", URI: uri})
		if !errors.Is(err, storage.ErrInvalid) {
			t.Errorf("AddContact(%q) error = %v, want ErrInvalid", uri, err)
		}
	}

	base := t.TempDir()
	dir := filepath.Join(base, "baresip")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(base, "contacts")
	if err := os.WriteFile(outside, []byte(`<sip:existing@example.com>`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "contacts")); err != nil {
		t.Fatal(err)
	}
	symlinkStore := storage.New(dir)

	list, err := symlinkStore.ListContacts()
	if err != nil {
		t.Fatalf("ListContacts() through symlink error = %v", err)
	}
	if len(list.Contacts) != 1 || list.Contacts[0].URI != "sip:existing@example.com" {
		t.Fatalf("ListContacts() through symlink = %#v", list)
	}
	if err := symlinkStore.AddContact(storage.Contact{URI: "sip:new@example.com"}); !errors.Is(err, storage.ErrSymlink) {
		t.Fatalf("AddContact() through symlink error = %v, want ErrSymlink", err)
	}
	if err := symlinkStore.RemoveContact("sip:existing@example.com"); !errors.Is(err, storage.ErrSymlink) {
		t.Fatalf("RemoveContact() through symlink error = %v, want ErrSymlink", err)
	}
	if got := readFile(t, outside); got != `<sip:existing@example.com>`+"\n" {
		t.Fatalf("contacts symlink target changed to %q", got)
	}
}

func TestConcurrentContactAddsAreSerialized(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "baresip")
	const count = 32
	start := make(chan struct{})
	errs := make(chan error, count)
	var wg sync.WaitGroup
	for i := 0; i < count; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			store := storage.New(dir)
			errs <- store.AddContact(storage.Contact{
				Name: fmt.Sprintf("Contact %d", i),
				URI:  fmt.Sprintf("sip:%d@example.com", i),
			})
		}(i)
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent AddContact() error = %v", err)
		}
	}

	list, err := storage.New(dir).ListContacts()
	if err != nil {
		t.Fatalf("ListContacts() error = %v", err)
	}
	if len(list.Contacts) != count {
		t.Fatalf("concurrent contact count = %d, want %d", len(list.Contacts), count)
	}
	seen := make(map[string]bool, count)
	for _, contact := range list.Contacts {
		seen[contact.URI] = true
	}
	for i := 0; i < count; i++ {
		uri := fmt.Sprintf("sip:%d@example.com", i)
		if !seen[uri] {
			t.Errorf("missing contact %s", uri)
		}
	}
	assertMode(t, filepath.Join(dir, "contacts"), 0o600)
	assertNoTemporaryFiles(t, dir)
}

func TestAudioConfigPreservesUnrelatedLinesAndUpdatesDuplicates(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "baresip")
	if err := os.Mkdir(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "config")
	original := "# audio comment\n" +
		"poll_method             epoll\n" +
		"audio_player            pipewire,old-output\n" +
		"module                  opus.so\n" +
		"audio_player pipewire,second-output\n" +
		"audio_source            pipewire,old-input\n"
	if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	store := storage.New(dir)

	before, err := store.ReadAudioConfig()
	if err != nil {
		t.Fatalf("ReadAudioConfig() error = %v", err)
	}
	if before != (storage.AudioConfig{Output: "second-output", Input: "old-input"}) {
		t.Fatalf("ReadAudioConfig() = %#v", before)
	}

	if err := store.WriteAudioConfig(storage.AudioConfig{Output: "sink.node", Input: "source.node"}); err != nil {
		t.Fatalf("WriteAudioConfig() error = %v", err)
	}
	got := readFile(t, path)
	for _, preserved := range []string{"# audio comment", "poll_method             epoll", "module                  opus.so"} {
		if !strings.Contains(got, preserved) {
			t.Errorf("WriteAudioConfig() lost %q:\n%s", preserved, got)
		}
	}
	if count := strings.Count(got, "audio_player            pipewire,sink.node"); count != 2 {
		t.Errorf("updated audio_player count = %d, want 2:\n%s", count, got)
	}
	for _, line := range []string{
		"audio_source            pipewire,source.node",
		"audio_alert             pipewire,sink.node",
	} {
		if !strings.Contains(got, line) {
			t.Errorf("config does not contain %q:\n%s", line, got)
		}
	}
	assertMode(t, dir, 0o700)
	assertMode(t, path, 0o644)

	after, err := store.ReadAudioConfig()
	if err != nil {
		t.Fatalf("ReadAudioConfig() after write error = %v", err)
	}
	if after != (storage.AudioConfig{Output: "sink.node", Input: "source.node"}) {
		t.Fatalf("ReadAudioConfig() after write = %#v", after)
	}
	assertNoTemporaryFiles(t, dir)
}

func TestAudioConfigRejectsInvalidMissingAndSymlinkWrites(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "baresip")
	store := storage.New(dir)
	if err := store.WriteAudioConfig(storage.AudioConfig{}); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("missing WriteAudioConfig() error = %v, want ErrNotFound", err)
	}
	for _, device := range []string{"node with space", "node,with-comma", "line\nbreak", strings.Repeat("x", storage.MaxFieldLength+1)} {
		if err := store.WriteAudioConfig(storage.AudioConfig{Output: device}); !errors.Is(err, storage.ErrInvalid) {
			t.Errorf("WriteAudioConfig(%q) error = %v, want ErrInvalid", device, err)
		}
	}

	base := t.TempDir()
	dir = filepath.Join(base, "baresip")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(base, "config")
	if err := os.WriteFile(outside, []byte("audio_player pipewire\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "config")); err != nil {
		t.Fatal(err)
	}
	err := storage.New(dir).WriteAudioConfig(storage.AudioConfig{Output: "sink.node"})
	if !errors.Is(err, storage.ErrSymlink) {
		t.Fatalf("symlink WriteAudioConfig() error = %v, want ErrSymlink", err)
	}
	if got := readFile(t, outside); got != "audio_player pipewire\n" {
		t.Fatalf("config symlink target changed to %q", got)
	}
}

func TestCallHistoryRoundTripLimitAndSafety(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "baresip")
	store := storage.New(dir)
	if history, err := store.ReadCallHistory(); err != nil || len(history) != 0 {
		t.Fatalf("missing ReadCallHistory() = %#v, %v", history, err)
	}

	at := time.Date(2026, 5, 6, 7, 8, 9, 0, time.UTC)
	entries := make([]app.CallHistoryEntry, storage.MaxCallHistoryEntries+5)
	for i := range entries {
		entries[i] = app.CallHistoryEntry{
			Direction:   app.CallDirectionOutgoing,
			Outcome:     app.CallOutcomeConnected,
			Peer:        fmt.Sprintf("Peer %d", i),
			Target:      fmt.Sprintf("sip:%d@example.com", i),
			StartedAt:   at.Add(time.Duration(i) * time.Minute),
			ConnectedAt: at.Add(time.Duration(i)*time.Minute + time.Second),
			EndedAt:     at.Add(time.Duration(i)*time.Minute + time.Minute),
		}
	}
	if err := store.WriteCallHistory(entries); err != nil {
		t.Fatalf("WriteCallHistory() error = %v", err)
	}
	got, err := store.ReadCallHistory()
	if err != nil {
		t.Fatalf("ReadCallHistory() error = %v", err)
	}
	if len(got) != storage.MaxCallHistoryEntries || got[0] != entries[0] || got[len(got)-1] != entries[storage.MaxCallHistoryEntries-1] {
		t.Fatalf("ReadCallHistory() retained wrong entries: len=%d first=%#v last=%#v", len(got), got[0], got[len(got)-1])
	}
	assertMode(t, store.Paths().History, 0o600)
	assertNoTemporaryFiles(t, dir)

	if err := os.WriteFile(store.Paths().History, []byte("not json\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ReadCallHistory(); !errors.Is(err, storage.ErrInvalid) {
		t.Fatalf("malformed ReadCallHistory() error = %v, want ErrInvalid", err)
	}

	outside := filepath.Join(t.TempDir(), "outside-history")
	if err := os.WriteFile(outside, []byte("keep\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(store.Paths().History); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, store.Paths().History); err != nil {
		t.Fatal(err)
	}
	if err := store.WriteCallHistory(entries[:1]); !errors.Is(err, storage.ErrSymlink) {
		t.Fatalf("symlink WriteCallHistory() error = %v, want ErrSymlink", err)
	}
	if got := readFile(t, outside); got != "keep\n" {
		t.Fatalf("history symlink target changed to %q", got)
	}
}

func TestNewWithPathsRejectsFilesOutsideDirectory(t *testing.T) {
	dir := t.TempDir()
	_, err := storage.NewWithPaths(storage.Paths{
		Dir:      dir,
		Accounts: filepath.Join(dir, "accounts"),
		Contacts: filepath.Join(dir, "contacts"),
		Config:   filepath.Join(dir, "config"),
		History:  filepath.Join(dir, "gosiptea-call-history.json"),
		Lock:     filepath.Join(filepath.Dir(dir), "lock"),
	})
	if !errors.Is(err, storage.ErrInvalid) {
		t.Fatalf("NewWithPaths() error = %v, want ErrInvalid", err)
	}
}

func assertMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat(%s) error = %v", path, err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Fatalf("mode of %s = %04o, want %04o", path, got, want)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%s) error = %v", path, err)
	}
	return string(data)
}

func assertNoTemporaryFiles(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir(%s) error = %v", dir, err)
	}
	for _, entry := range entries {
		name := entry.Name()
		if strings.HasPrefix(name, ".accounts.") || strings.HasPrefix(name, ".contacts.") || strings.HasPrefix(name, ".config.") || strings.HasPrefix(name, ".gosiptea-call-history.json.") {
			t.Errorf("temporary file remains: %s", name)
		}
	}
}
