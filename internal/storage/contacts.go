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

const contactsHeader = "#\n" +
	"# SIP contacts managed by GoSipTea.\n" +
	"# One contact per line: \"Display name\" <sip:user@host>;addr-params\n" +
	"# See baresip's modules/contact for the addr-params\n" +
	"# (;presence=, ;access=allow|block, ;audio=, ;video=).\n" +
	"#\n" +
	"\n"

var contactLinePattern = regexp.MustCompile(`^\s*"?([^"<>]*?)"?\s*<([^<>]+)>([^<>]*)\s*$`)

// Contact is one baresip contact. Params is populated when listing existing entries.
type Contact struct {
	Name   string
	URI    string
	Params string
}

// ContactList reports whether the contacts file exists and contains every parsed contact.
type ContactList struct {
	Configured bool
	Contacts   []Contact
}

// ListContacts reads contacts. It follows a contacts symlink to support dotfile-managed lists.
func (s *Store) ListContacts() (ContactList, error) {
	result := ContactList{Contacts: []Contact{}}
	if err := s.validate(); err != nil {
		return result, err
	}
	data, err := readRegularFile(s.paths.Contacts, true)
	if errors.Is(err, os.ErrNotExist) {
		return result, nil
	}
	if err != nil {
		return result, fmt.Errorf("read contacts: %w", err)
	}
	result.Configured = true
	result.Contacts = parseContacts(splitLines(data))
	return result, nil
}

// AddContact appends a contact without changing existing lines or parameters.
func (s *Store) AddContact(contact Contact) error {
	name := strings.TrimSpace(contact.Name)
	uri := strings.TrimSpace(contact.URI)
	if contact.Params != "" {
		return fmt.Errorf("%w: params cannot be set when adding a contact", ErrInvalid)
	}
	if err := validateContactName(name); err != nil {
		return err
	}
	if err := validateContactURI(uri); err != nil {
		return err
	}

	return s.withExclusiveLock(func() error {
		if err := refuseSymlinkOrSpecial(s.paths.Contacts, true); err != nil {
			return fmt.Errorf("write contacts: %w", err)
		}

		mode := os.FileMode(0o600)
		lines := splitLines([]byte(contactsHeader))
		data, err := readRegularFile(s.paths.Contacts, false)
		if err == nil {
			lines = splitLines(data)
			info, statErr := os.Stat(s.paths.Contacts)
			if statErr != nil {
				return fmt.Errorf("inspect contacts mode: %w", statErr)
			}
			mode = info.Mode().Perm()
		} else if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("read contacts before write: %w", err)
		}

		for _, existing := range parseContacts(lines) {
			if existing.URI == uri {
				return fmt.Errorf("%w: contact already saved", ErrDuplicate)
			}
		}
		if name == "" {
			lines = append(lines, fmt.Sprintf("<%s>", uri))
		} else {
			lines = append(lines, fmt.Sprintf(`"%s" <%s>`, name, uri))
		}
		if err := atomicWrite(s.paths.Contacts, joinLines(lines), mode); err != nil {
			return fmt.Errorf("write contacts: %w", err)
		}
		return nil
	})
}

// RemoveContact removes every contact line whose URI exactly matches uri.
func (s *Store) RemoveContact(uri string) error {
	uri = strings.TrimSpace(uri)
	if err := validateRemovalURI(uri); err != nil {
		return err
	}

	return s.withExclusiveLock(func() error {
		if err := refuseSymlinkOrSpecial(s.paths.Contacts, false); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return fmt.Errorf("%w: no contacts file", ErrNotFound)
			}
			return fmt.Errorf("write contacts: %w", err)
		}
		data, err := readRegularFile(s.paths.Contacts, false)
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("%w: no contacts file", ErrNotFound)
		}
		if err != nil {
			return fmt.Errorf("read contacts before write: %w", err)
		}
		info, err := os.Stat(s.paths.Contacts)
		if err != nil {
			return fmt.Errorf("inspect contacts mode: %w", err)
		}

		removed := 0
		kept := make([]string, 0)
		for _, line := range splitLines(data) {
			contact, ok := parseContactLine(line)
			if ok && contact.URI == uri {
				removed++
				continue
			}
			kept = append(kept, line)
		}
		if removed == 0 {
			return fmt.Errorf("%w: contact not found", ErrNotFound)
		}
		if err := atomicWrite(s.paths.Contacts, joinLines(kept), info.Mode().Perm()); err != nil {
			return fmt.Errorf("write contacts: %w", err)
		}
		return nil
	})
}

func parseContacts(lines []string) []Contact {
	contacts := make([]Contact, 0)
	for _, line := range lines {
		contact, ok := parseContactLine(line)
		if ok {
			contacts = append(contacts, contact)
		}
	}
	return contacts
}

func parseContactLine(line string) (Contact, bool) {
	if strings.TrimSpace(line) == "" || strings.HasPrefix(strings.TrimLeftFunc(line, unicode.IsSpace), "#") {
		return Contact{}, false
	}
	match := contactLinePattern.FindStringSubmatch(line)
	if match == nil {
		return Contact{}, false
	}
	return Contact{
		Name:   strings.TrimSpace(match[1]),
		URI:    strings.TrimSpace(match[2]),
		Params: strings.TrimSpace(match[3]),
	}, true
}

func validateContactName(name string) error {
	if !utf8.ValidString(name) || utf8.RuneCountInString(name) > MaxContactName {
		return fmt.Errorf("%w: invalid contact name", ErrInvalid)
	}
	for _, char := range name {
		if char <= 0x1f || strings.ContainsRune(`"<>;\\`, char) {
			return fmt.Errorf("%w: invalid contact name", ErrInvalid)
		}
	}
	return nil
}

func validateRemovalURI(uri string) error {
	if !utf8.ValidString(uri) || uri == "" || utf8.RuneCountInString(uri) > MaxFieldLength {
		return fmt.Errorf("%w: invalid contact address", ErrInvalid)
	}
	lower := strings.ToLower(uri)
	if !strings.HasPrefix(lower, "sip:") && !strings.HasPrefix(lower, "sips:") {
		return fmt.Errorf("%w: invalid contact address", ErrInvalid)
	}
	for _, char := range uri {
		if unicode.IsControl(char) || unicode.IsSpace(char) || strings.ContainsRune(`<>"`, char) {
			return fmt.Errorf("%w: invalid contact address", ErrInvalid)
		}
	}
	return nil
}

func validateContactURI(uri string) error {
	if !utf8.ValidString(uri) || uri == "" || utf8.RuneCountInString(uri) > MaxFieldLength {
		return fmt.Errorf("%w: enter an address like sip:user@host", ErrInvalid)
	}
	var address string
	switch {
	case strings.HasPrefix(uri, "sip:"):
		address = strings.TrimPrefix(uri, "sip:")
	case strings.HasPrefix(uri, "sips:"):
		address = strings.TrimPrefix(uri, "sips:")
	default:
		return fmt.Errorf("%w: enter an address like sip:user@host", ErrInvalid)
	}
	if strings.Count(address, "@") != 1 {
		return fmt.Errorf("%w: enter an address like sip:user@host", ErrInvalid)
	}
	parts := strings.SplitN(address, "@", 2)
	if parts[0] == "" || parts[1] == "" {
		return fmt.Errorf("%w: enter an address like sip:user@host", ErrInvalid)
	}
	for _, char := range address {
		if char <= 0x1f || unicode.IsSpace(char) || strings.ContainsRune(`<>;"`, char) {
			return fmt.Errorf("%w: enter an address like sip:user@host", ErrInvalid)
		}
	}
	return nil
}
