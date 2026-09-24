package storage

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

const (
	MaxFieldLength = 255
	MaxContactName = 64
	MaxFileSize    = 1 << 20
	LockTimeout    = 5 * time.Second
	lockRetryDelay = 25 * time.Millisecond
)

var (
	ErrDuplicate   = errors.New("storage: duplicate entry")
	ErrInvalid     = errors.New("storage: invalid input")
	ErrNotFound    = errors.New("storage: not found")
	ErrNotRegular  = errors.New("storage: path is not a regular file")
	ErrSymlink     = errors.New("storage: refusing to write through a symlink")
	ErrTooLarge    = errors.New("storage: file is too large")
	ErrLockTimeout = errors.New("storage: timed out waiting for the file lock")
)

// Paths names every file used by Store. All files must be direct children of Dir.
type Paths struct {
	Dir      string
	Accounts string
	Contacts string
	Config   string
	History  string
	Lock     string
}

// Store persists the subset of baresip state managed by GoSipTea.
type Store struct {
	paths Paths
}

// New creates a Store rooted at dir. It does not touch the filesystem.
func New(dir string) *Store {
	dir = filepath.Clean(dir)
	return &Store{paths: Paths{
		Dir:      dir,
		Accounts: filepath.Join(dir, "accounts"),
		Contacts: filepath.Join(dir, "contacts"),
		Config:   filepath.Join(dir, "config"),
		History:  filepath.Join(dir, "gosiptea-call-history.json"),
		Lock:     filepath.Join(dir, ".gosiptea.lock"),
	}}
}

// NewWithPaths creates a Store with explicit paths. Atomic replacement requires
// every managed file to live directly in Dir.
func NewWithPaths(paths Paths) (*Store, error) {
	paths.Dir = filepath.Clean(paths.Dir)
	paths.Accounts = filepath.Clean(paths.Accounts)
	paths.Contacts = filepath.Clean(paths.Contacts)
	paths.Config = filepath.Clean(paths.Config)
	paths.History = filepath.Clean(paths.History)
	paths.Lock = filepath.Clean(paths.Lock)

	if paths.Dir == "." && paths.Accounts == "." && paths.Contacts == "." && paths.Config == "." && paths.History == "." && paths.Lock == "." {
		return nil, fmt.Errorf("%w: paths are empty", ErrInvalid)
	}
	for name, path := range map[string]string{
		"accounts": paths.Accounts,
		"contacts": paths.Contacts,
		"config":   paths.Config,
		"history":  paths.History,
		"lock":     paths.Lock,
	} {
		if path == "." || filepath.Dir(path) != paths.Dir {
			return nil, fmt.Errorf("%w: %s must be a direct child of %s", ErrInvalid, name, paths.Dir)
		}
	}
	return &Store{paths: paths}, nil
}

// Default returns a Store for ~/.baresip. It does not create or read that directory.
func Default() (*Store, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("find home directory: %w", err)
	}
	return New(filepath.Join(home, ".baresip")), nil
}

// Paths returns the paths configured for this Store.
func (s *Store) Paths() Paths {
	if s == nil {
		return Paths{}
	}
	return s.paths
}

func (s *Store) validate() error {
	if s == nil || s.paths.Dir == "" || s.paths.Accounts == "" || s.paths.Contacts == "" || s.paths.Config == "" || s.paths.History == "" || s.paths.Lock == "" {
		return fmt.Errorf("%w: incomplete store paths", ErrInvalid)
	}
	return nil
}

func (s *Store) ensureSecureDir() error {
	if err := s.validate(); err != nil {
		return err
	}

	info, err := os.Lstat(s.paths.Dir)
	if errors.Is(err, os.ErrNotExist) {
		if err := os.MkdirAll(s.paths.Dir, 0o700); err != nil {
			return fmt.Errorf("create baresip directory: %w", err)
		}
		info, err = os.Lstat(s.paths.Dir)
	}
	if err != nil {
		return fmt.Errorf("inspect baresip directory: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%w: %s", ErrSymlink, s.paths.Dir)
	}
	if !info.IsDir() {
		return fmt.Errorf("%w: %s", ErrNotRegular, s.paths.Dir)
	}
	if err := os.Chmod(s.paths.Dir, 0o700); err != nil {
		return fmt.Errorf("set baresip directory mode: %w", err)
	}
	return nil
}

func (s *Store) withExclusiveLock(fn func() error) (retErr error) {
	if err := s.ensureSecureDir(); err != nil {
		return err
	}
	if err := refuseSymlinkOrSpecial(s.paths.Lock, true); err != nil {
		return fmt.Errorf("open storage lock: %w", err)
	}

	fd, err := syscall.Open(s.paths.Lock, syscall.O_WRONLY|syscall.O_CREAT|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		if errors.Is(err, syscall.ELOOP) {
			return fmt.Errorf("open storage lock: %w", ErrSymlink)
		}
		return fmt.Errorf("open storage lock: %w", err)
	}
	file := os.NewFile(uintptr(fd), s.paths.Lock)
	if file == nil {
		syscall.Close(fd)
		return errors.New("open storage lock: invalid file descriptor")
	}
	defer func() {
		if err := syscall.Flock(fd, syscall.LOCK_UN); retErr == nil && err != nil {
			retErr = fmt.Errorf("unlock storage: %w", err)
		}
		if err := file.Close(); retErr == nil && err != nil {
			retErr = fmt.Errorf("close storage lock: %w", err)
		}
	}()

	info, err := file.Stat()
	if err != nil {
		return fmt.Errorf("inspect storage lock: %w", err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("inspect storage lock: %w", ErrNotRegular)
	}
	if err := file.Chmod(0o600); err != nil {
		return fmt.Errorf("set storage lock mode: %w", err)
	}
	deadline := time.Now().Add(LockTimeout)
	for {
		err = syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			break
		}
		if errors.Is(err, syscall.EINTR) {
			continue
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EAGAIN) {
			return fmt.Errorf("lock storage: %w", err)
		}
		if time.Now().After(deadline) {
			return ErrLockTimeout
		}
		time.Sleep(lockRetryDelay)
	}
	return fn()
}

func refuseSymlinkOrSpecial(path string, allowMissing bool) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) && allowMissing {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%w: %s", ErrSymlink, path)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("%w: %s", ErrNotRegular, path)
	}
	return nil
}

func readRegularFile(path string, followSymlink bool) ([]byte, error) {
	if !followSymlink {
		if err := refuseSymlinkOrSpecial(path, false); err != nil {
			return nil, err
		}
	}

	flags := syscall.O_RDONLY | syscall.O_CLOEXEC | syscall.O_NONBLOCK
	if !followSymlink {
		flags |= syscall.O_NOFOLLOW
	}
	fd, err := syscall.Open(path, flags, 0)
	if err != nil {
		if errors.Is(err, syscall.ENOENT) {
			return nil, os.ErrNotExist
		}
		if errors.Is(err, syscall.ELOOP) {
			return nil, fmt.Errorf("%w: %s", ErrSymlink, path)
		}
		return nil, err
	}
	file := os.NewFile(uintptr(fd), path)
	if file == nil {
		syscall.Close(fd)
		return nil, errors.New("invalid file descriptor")
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%w: %s", ErrNotRegular, path)
	}
	data, err := io.ReadAll(io.LimitReader(file, MaxFileSize+1))
	if err != nil {
		return nil, err
	}
	if len(data) > MaxFileSize {
		return nil, fmt.Errorf("%w: %s", ErrTooLarge, path)
	}
	return data, nil
}

func atomicWrite(path string, data []byte, mode os.FileMode) (retErr error) {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".*")
	if err != nil {
		return fmt.Errorf("create temporary file: %w", err)
	}
	tmpName := tmp.Name()
	renamed := false
	defer func() {
		if !renamed {
			_ = os.Remove(tmpName)
		}
	}()
	defer func() {
		if tmp != nil {
			if err := tmp.Close(); retErr == nil && err != nil {
				retErr = fmt.Errorf("close temporary file: %w", err)
			}
		}
	}()

	if err := tmp.Chmod(mode); err != nil {
		return fmt.Errorf("set temporary file mode: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		return fmt.Errorf("write temporary file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		return fmt.Errorf("sync temporary file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		tmp = nil
		return fmt.Errorf("close temporary file: %w", err)
	}
	tmp = nil

	if err := refuseSymlinkOrSpecial(path, true); err != nil {
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("replace %s: %w", path, err)
	}
	renamed = true

	dirFile, err := os.Open(dir)
	if err != nil {
		return fmt.Errorf("open directory for sync: %w", err)
	}
	defer dirFile.Close()
	if err := dirFile.Sync(); err != nil {
		return fmt.Errorf("sync directory: %w", err)
	}
	return nil
}

func splitLines(data []byte) []string {
	if len(data) == 0 {
		return nil
	}
	text := string(data)
	lines := make([]string, 0)
	start := 0
	for i := 0; i < len(text); i++ {
		if text[i] != '\n' {
			continue
		}
		line := text[start:i]
		if len(line) > 0 && line[len(line)-1] == '\r' {
			line = line[:len(line)-1]
		}
		lines = append(lines, line)
		start = i + 1
	}
	if start < len(text) {
		line := text[start:]
		if len(line) > 0 && line[len(line)-1] == '\r' {
			line = line[:len(line)-1]
		}
		lines = append(lines, line)
	}
	return lines
}

func joinLines(lines []string) []byte {
	result := make([]byte, 0)
	for _, line := range lines {
		result = append(result, line...)
		result = append(result, '\n')
	}
	return result
}
