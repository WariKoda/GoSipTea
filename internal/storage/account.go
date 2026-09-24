package storage

import (
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

const accountHeader = "# SIP account — managed by the oma.sip plugin (widget or setup.sh).\n" +
	"# Contains the extension password: 600 permission required.\n" +
	"# Default is TLS + SRTP; unencrypted transport only by explicit choice\n" +
	"# (the widget overwrites this line when saving the account).\n"

var (
	accountURIPattern = regexp.MustCompile(`<sips?:([^@>]+)@([^;>]+)([^>]*)>`)
	authPassPattern   = regexp.MustCompile(`auth_pass=([^;]*)`)
	authUserPattern   = regexp.MustCompile(`auth_user=([^;]*)`)
	outboundPattern   = regexp.MustCompile(`outbound="sips?:([^";]+)([^"]*)"`)
	transportPattern  = regexp.MustCompile(`(?i)(?:^|;)transport=(udp|tcp|tls)(?:;|$)`)
)

// Account is the non-secret account state returned by ReadAccount.
type Account struct {
	Configured  bool
	Username    string
	Domain      string
	Server      string
	Login       string
	HasPassword bool
	Secure      bool
	Transport   string
}

// AccountCredentials contains account values accepted by WriteAccount.
// A nil Secure value selects TLS and SRTP. An empty Password keeps the saved password.
// When Secure is false, an existing TCP transport is retained.
type AccountCredentials struct {
	Server   string
	Username string
	Domain   string
	Login    string
	Password string
	Secure   *bool
}

// ReadAccount reads the first valid SIP account. It never returns the password.
func (s *Store) ReadAccount() (Account, error) {
	account := Account{Secure: true}
	if err := s.validate(); err != nil {
		return account, err
	}
	data, err := readRegularFile(s.paths.Accounts, false)
	if errors.Is(err, os.ErrNotExist) {
		return account, nil
	}
	if err != nil {
		return account, fmt.Errorf("read accounts: %w", err)
	}
	parsed, _ := parseAccount(data)
	return parsed, nil
}

// WriteAccount replaces the managed account after taking the shared storage lock.
func (s *Store) WriteAccount(input AccountCredentials) error {
	return s.withExclusiveLock(func() error {
		if err := refuseSymlinkOrSpecial(s.paths.Accounts, true); err != nil {
			return fmt.Errorf("write accounts: %w", err)
		}

		currentPassword := ""
		var current Account
		data, err := readRegularFile(s.paths.Accounts, false)
		if err == nil {
			current, currentPassword = parseAccount(data)
		} else if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("read accounts before write: %w", err)
		}

		server := strings.TrimSpace(input.Server)
		username := strings.TrimSpace(input.Username)
		domain := strings.TrimSpace(input.Domain)
		login := strings.TrimSpace(input.Login)
		password := input.Password
		if domain == "" {
			domain = server
		}
		if login == "" {
			login = username
		}
		if password == "" {
			password = currentPassword
		}
		if server == "" || username == "" {
			return fmt.Errorf("%w: server and username are required", ErrInvalid)
		}
		if password == "" {
			return fmt.Errorf("%w: set a password because none is saved", ErrInvalid)
		}
		fields := []struct {
			name  string
			value string
		}{
			{"server", server},
			{"username", username},
			{"domain", domain},
			{"login", login},
			{"password", password},
		}
		for _, field := range fields {
			if err := validateAccountField(field.name, field.value); err != nil {
				return err
			}
		}

		secure := true
		if input.Secure != nil {
			secure = *input.Secure
		}
		uri := fmt.Sprintf("<sip:%s@%s>", username, domain)
		outbound := fmt.Sprintf(`outbound="sip:%s"`, server)
		if secure {
			uri = fmt.Sprintf("<sip:%s@%s;transport=tls>", username, domain)
			outbound = fmt.Sprintf(`outbound="sip:%s;transport=tls";mediaenc=srtp`, server)
		} else if current.Transport == "tcp" {
			uri = fmt.Sprintf("<sip:%s@%s;transport=tcp>", username, domain)
			outbound = fmt.Sprintf(`outbound="sip:%s;transport=tcp"`, server)
		}
		line := fmt.Sprintf("%s;auth_user=%s;auth_pass=%s;%s;answermode=manual;regint=300;fbregint=30;audio_codecs=opus/48000/2,pcma,pcmu\n",
			uri, login, password, outbound)
		if err := atomicWrite(s.paths.Accounts, []byte(accountHeader+line), 0o600); err != nil {
			return fmt.Errorf("write accounts: %w", err)
		}
		return nil
	})
}

func parseAccount(data []byte) (Account, string) {
	account := Account{Secure: true}
	for _, rawLine := range splitLines(data) {
		line := strings.TrimSpace(rawLine)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		match := accountURIPattern.FindStringSubmatch(line)
		if match == nil {
			continue
		}
		account.Configured = true
		account.Username = match[1]
		account.Domain = match[2]
		account.Transport = "udp"
		if transport := transportPattern.FindStringSubmatch(match[3]); transport != nil {
			account.Transport = strings.ToLower(transport[1])
		}
		account.Secure = strings.Contains(line, "transport=tls") && strings.Contains(line, "mediaenc=srtp")

		password := ""
		if match := authPassPattern.FindStringSubmatch(line); match != nil {
			password = match[1]
			account.HasPassword = password != ""
		}
		if match := authUserPattern.FindStringSubmatch(line); match != nil {
			account.Login = match[1]
		}
		if match := outboundPattern.FindStringSubmatch(line); match != nil {
			account.Server = match[1]
			if transport := transportPattern.FindStringSubmatch(match[2]); transport != nil {
				account.Transport = strings.ToLower(transport[1])
			}
		}
		return account, password
	}
	return account, ""
}

func validateAccountField(name, value string) error {
	if !utf8.ValidString(value) {
		return fmt.Errorf("%w: %s is not valid UTF-8", ErrInvalid, name)
	}
	if utf8.RuneCountInString(value) > MaxFieldLength {
		return fmt.Errorf("%w: %s is too long", ErrInvalid, name)
	}
	for _, char := range value {
		if char <= 0x1f || unicode.IsSpace(char) || strings.ContainsRune(`;"<>`, char) {
			return fmt.Errorf("%w: %s contains an unsupported character", ErrInvalid, name)
		}
	}
	return nil
}
