package app

import (
	"net/url"
	"regexp"
	"sort"
	"strings"
	"unicode"
)

// Contact is the subset of a baresip contact used by the domain package.
type Contact struct {
	URI  string
	Name string
}

type callerAddress struct {
	user string
	host string
}

var contactURIPattern = regexp.MustCompile(`(?i)^sips?:[^<>\s;"@]+@[^<>\s;"]+$`)

// NormalizeContactURI converts a full SIP address or extension into the format
// expected by baresip's contacts file. Extensions require an account domain.
func NormalizeContactURI(raw, domain string) string {
	value := strings.TrimSpace(raw)
	if value == "" || len([]rune(value)) > 200 {
		return ""
	}
	lower := strings.ToLower(value)
	if strings.HasPrefix(lower, "sip:") || strings.HasPrefix(lower, "sips:") {
		if strings.Count(value, "@") == 1 && contactURIPattern.MatchString(value) {
			return value
		}
		return ""
	}
	if strings.Contains(value, "@") && !strings.HasPrefix(value, "@") {
		value = "sip:" + value
		if strings.Count(value, "@") == 1 && contactURIPattern.MatchString(value) {
			return value
		}
		return ""
	}

	var extension strings.Builder
	for _, char := range value {
		if char >= '0' && char <= '9' || char == '+' || char == '*' || char == '#' {
			extension.WriteRune(char)
		}
	}
	host := strings.TrimSpace(domain)
	if extension.Len() == 0 || host == "" {
		return ""
	}
	value = "sip:" + extension.String() + "@" + host
	if !contactURIPattern.MatchString(value) {
		return ""
	}
	return value
}

// PeerDisplay returns a short, bounded label for a SIP peer.
func PeerDisplay(uri string) string {
	text := ClampText(uri, MaxPeerInputLength)
	display := ""

	if open := strings.Index(text, "<"); open >= 0 {
		if close := strings.LastIndex(text, ">"); close > open {
			display = strings.TrimSpace(strings.Trim(strings.TrimSpace(text[:open]), `"`))
			text = text[open+1 : close]
		}
	}

	text = strings.TrimSpace(strings.Trim(text, "<>"))
	lower := strings.ToLower(text)
	switch {
	case strings.HasPrefix(lower, "sips:"):
		text = text[len("sips:"):]
	case strings.HasPrefix(lower, "sip:"):
		text = text[len("sip:"):]
	}

	user := text
	if at := strings.LastIndexByte(user, '@'); at > 0 {
		user = user[:at]
	}
	if semicolon := strings.IndexByte(user, ';'); semicolon >= 0 {
		user = user[:semicolon]
	}
	if decoded, err := url.PathUnescape(user); err == nil {
		user = decoded
	}

	if display == "" {
		return ClampText(user, MaxPeerDisplayLength)
	}
	return ClampText(display+" ("+user+")", MaxPeerDisplayLength)
}

// CallerName resolves an incoming peer against contacts. Exact SIP addresses
// win. A formatted international number may match across hosts, but ambiguous
// matches fall back to the provider display name or peer address.
func CallerName(contacts []Contact, peerURI, displayName, countryCallingCode string) string {
	if name, ok := ContactName(contacts, peerURI, countryCallingCode); ok {
		return name
	}
	fallbackSource := displayName
	if fallbackSource == "" {
		fallbackSource = peerURI
	}
	return PeerDisplay(fallbackSource)
}

// ContactName returns the unambiguous contact name for a SIP address or a
// dialed number without host. Numbers without host only match by number.
func ContactName(contacts []Contact, target, countryCallingCode string) (string, bool) {
	caller, ok := parseCallerAddress(target)
	if !ok {
		return numberContactName(contacts, target, countryCallingCode)
	}
	callerNumber := normalizeCallerNumber(caller.user, countryCallingCode)

	bestScore := 0
	bestName := ""
	ambiguous := false
	for _, contact := range contacts {
		address, valid := parseCallerAddress(contact.URI)
		if !valid {
			continue
		}

		score := 0
		if caller.user == address.user && caller.host == address.host {
			score = 3
		} else if callerNumber != "" && callerNumber == normalizeCallerNumber(address.user, countryCallingCode) {
			if caller.host == address.host {
				score = 2
			} else if isInternationalNumber(callerNumber) {
				score = 1
			}
		}
		if score == 0 || score < bestScore {
			continue
		}

		name := strings.TrimSpace(ClampText(contact.Name, MaxPeerDisplayLength))
		if score > bestScore {
			bestScore = score
			bestName = name
			ambiguous = false
		} else if name != bestName {
			ambiguous = true
		}
	}

	if bestName != "" && !ambiguous {
		return bestName, true
	}
	return "", false
}

func numberContactName(contacts []Contact, target, countryCallingCode string) (string, bool) {
	if strings.Contains(target, "@") {
		return "", false
	}
	number := normalizeCallerNumber(strings.TrimSpace(target), countryCallingCode)
	if number == "" {
		return "", false
	}
	found := ""
	for _, contact := range contacts {
		address, valid := parseCallerAddress(contact.URI)
		if !valid || normalizeCallerNumber(address.user, countryCallingCode) != number {
			continue
		}
		name := strings.TrimSpace(ClampText(contact.Name, MaxPeerDisplayLength))
		if name == "" {
			continue
		}
		if found != "" && found != name {
			return "", false
		}
		found = name
	}
	return found, found != ""
}

// ResolveHistoryPeers replaces stored peer labels with current contact names.
// Entries without a matching contact keep the label captured at call time.
func ResolveHistoryPeers(entries []CallHistoryEntry, contacts []Contact, countryCallingCode string) []CallHistoryEntry {
	resolved := append([]CallHistoryEntry(nil), entries...)
	for i := range resolved {
		if name, ok := ContactName(contacts, resolved[i].Target, countryCallingCode); ok {
			resolved[i].Peer = name
		}
	}
	return resolved
}

// IncomingCaller is an alias matching the corresponding Model.js operation.
func IncomingCaller(contacts []Contact, peerURI, displayName, countryCallingCode string) string {
	return CallerName(contacts, peerURI, displayName, countryCallingCode)
}

// FuzzyScore returns -1 when needle is not a subsequence of text. Higher
// scores favor exact matches, prefixes, adjacent characters, and word starts.
func FuzzyScore(needle, text string) int {
	query := []rune(strings.ToLower(strings.TrimSpace(needle)))
	haystack := []rune(strings.ToLower(text))
	if len(query) == 0 {
		return 0
	}
	if len(haystack) == 0 {
		return -1
	}
	if string(query) == string(haystack) {
		return 1000
	}

	score := 0
	queryIndex := 0
	previous := -2
	for i, r := range haystack {
		if queryIndex >= len(query) {
			break
		}
		if r != query[queryIndex] {
			continue
		}
		score += 10
		if i == previous+1 {
			score += 10
		}
		if i == 0 || !unicode.IsLetter(haystack[i-1]) && !unicode.IsDigit(haystack[i-1]) {
			score += 15
		}
		previous = i
		queryIndex++
	}
	if queryIndex < len(query) {
		return -1
	}

	if start := runeSliceIndex(haystack, query); start == 0 {
		score += 200
	} else if start > 0 {
		score += 100
	}
	lengthPenalty := len(haystack)
	if lengthPenalty > 50 {
		lengthPenalty = 50
	}
	return score - lengthPenalty
}

// FilterContacts returns matching contacts in descending relevance. Equal
// scores retain file order.
func FilterContacts(contacts []Contact, query string) []Contact {
	if strings.TrimSpace(query) == "" {
		return append([]Contact(nil), contacts...)
	}

	type scoredContact struct {
		contact Contact
		score   int
		order   int
	}
	scored := make([]scoredContact, 0, len(contacts))
	for i, contact := range contacts {
		uriScore := FuzzyScore(query, contact.URI)
		score := max(
			FuzzyScore(query, contact.Name),
			FuzzyScore(query, contact.URI+" "+contact.Name),
			uriScore-10,
		)
		if score >= 0 {
			scored = append(scored, scoredContact{contact: contact, score: score, order: i})
		}
	}
	sort.SliceStable(scored, func(i, j int) bool {
		if scored[i].score == scored[j].score {
			return scored[i].order < scored[j].order
		}
		return scored[i].score > scored[j].score
	})

	filtered := make([]Contact, len(scored))
	for i, item := range scored {
		filtered[i] = item.contact
	}
	return filtered
}

func parseCallerAddress(raw string) (callerAddress, bool) {
	text := strings.TrimSpace(raw)
	if text == "" || len([]rune(text)) > MaxPeerInputLength {
		return callerAddress{}, false
	}
	if open := strings.IndexByte(text, '<'); open >= 0 {
		close := strings.IndexByte(text[open+1:], '>')
		if close < 0 {
			return callerAddress{}, false
		}
		text = text[open+1 : open+1+close]
	}

	lower := strings.ToLower(text)
	switch {
	case strings.HasPrefix(lower, "sips:"):
		text = text[len("sips:"):]
	case strings.HasPrefix(lower, "sip:"):
		text = text[len("sip:"):]
	}
	if headers := strings.IndexByte(text, '?'); headers >= 0 {
		text = text[:headers]
	}

	at := strings.LastIndexByte(text, '@')
	if at <= 0 || at == len(text)-1 || strings.Contains(text[:at], "@") {
		return callerAddress{}, false
	}
	user := text[:at]
	host := text[at+1:]
	if parameters := strings.IndexByte(host, ';'); parameters >= 0 {
		host = host[:parameters]
	}
	if host == "" {
		return callerAddress{}, false
	}

	decoded, err := url.PathUnescape(user)
	if err != nil || decoded == "" {
		return callerAddress{}, false
	}
	return callerAddress{user: decoded, host: strings.ToLower(host)}, true
}

func normalizeCallerNumber(user, countryCallingCode string) string {
	if user == "" {
		return ""
	}
	runes := []rune(user)
	firstDigit := 0
	if runes[0] == '+' {
		firstDigit = 1
	}
	if firstDigit == len(runes) || runes[firstDigit] < '0' || runes[firstDigit] > '9' {
		return ""
	}
	for i, r := range runes {
		if r >= '0' && r <= '9' || r == ' ' || r == '(' || r == ')' || r == '.' || r == '-' || r == '+' && i == 0 {
			continue
		}
		return ""
	}

	number := strings.NewReplacer(" ", "", "(", "", ")", "", ".", "", "-", "").Replace(user)
	if strings.HasPrefix(number, "00") {
		number = "+" + number[2:]
	}

	country := strings.TrimPrefix(strings.TrimSpace(countryCallingCode), "+")
	if validCountryCallingCode(country) && len(number) >= 7 && number[0] == '0' && number[1] >= '1' && number[1] <= '9' {
		number = "+" + country + number[1:]
	}
	return number
}

func validCountryCallingCode(country string) bool {
	if len(country) < 1 || len(country) > 3 || country[0] == '0' {
		return false
	}
	for _, r := range country {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func isInternationalNumber(number string) bool {
	if len(number) < 8 || len(number) > 16 || number[0] != '+' || number[1] == '0' {
		return false
	}
	for _, r := range number[1:] {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func runeSliceIndex(text, query []rune) int {
	if len(query) > len(text) {
		return -1
	}
	for i := 0; i <= len(text)-len(query); i++ {
		if string(text[i:i+len(query)]) == string(query) {
			return i
		}
	}
	return -1
}
