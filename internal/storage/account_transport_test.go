package storage_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nibra/gosiptea/internal/storage"
)

func TestAccountSaveRetainsTCPTransport(t *testing.T) {
	for _, tc := range []struct {
		name     string
		uri      string
		outbound string
		want     string
	}{
		{"outbound TCP", "<sip:101@voice.example.com>", `outbound="sip:proxy.example.com;transport=tcp"`, "tcp"},
		{"account TCP", "<sip:101@voice.example.com;transport=tcp>", `outbound="sip:proxy.example.com"`, "tcp"},
		{"TCP with URI parameters", "<sip:101@voice.example.com>", `outbound="sip:proxy.example.com;transport=tcp;lr"`, "tcp"},
		{"explicit outbound overrides account", "<sip:101@voice.example.com;transport=tcp>", `outbound="sip:proxy.example.com;transport=udp"`, "udp"},
		{"default UDP", "<sip:101@voice.example.com>", `outbound="sip:proxy.example.com"`, "udp"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "accounts")
			line := tc.uri + ";auth_user=101;auth_pass=saved-secret;" + tc.outbound + "\n"
			if err := os.WriteFile(path, []byte(line), 0o600); err != nil {
				t.Fatal(err)
			}
			store := storage.New(dir)
			account, err := store.ReadAccount()
			if err != nil {
				t.Fatal(err)
			}
			if account.Transport != tc.want {
				t.Fatalf("transport = %q, want %q", account.Transport, tc.want)
			}
			secure := false
			input := storage.AccountCredentials{
				Server: account.Server, Username: account.Username,
				Domain: account.Domain, Login: account.Login, Secure: &secure,
			}
			// Repeated saves must not undo the transport chosen for this account.
			for i := 0; i < 2; i++ {
				if err := store.WriteAccount(input); err != nil {
					t.Fatal(err)
				}
				account, err = store.ReadAccount()
				if err != nil {
					t.Fatal(err)
				}
				if account.Transport != tc.want || account.Secure {
					t.Fatalf("saved account = %#v", account)
				}
				if !strings.Contains(readFile(t, path), "auth_pass=saved-secret;") {
					t.Fatal("saved password was not retained")
				}
			}
			assertMode(t, path, 0o600)

			secure = true
			if err := store.WriteAccount(input); err != nil {
				t.Fatal(err)
			}
			account, err = store.ReadAccount()
			if err != nil {
				t.Fatal(err)
			}
			if account.Transport != "tls" || !account.Secure {
				t.Fatal("explicit TLS selection must override retained transport")
			}
		})
	}
}
