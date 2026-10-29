package identity

import (
	"regexp"
	"strings"
	"unicode/utf8"
)

// Account identifier rules, shared by admin account creation and self
// registration (docs/15-m5-plan.md §11). The server is the only authority: the
// web client repeats these limits just to fail fast.
const (
	MinUsernameLen = 2
	MaxUsernameLen = 32
	MinPasswordLen = 8
	MaxPasswordLen = 128
	MaxEmailLen    = 254
)

var (
	// usernamePattern keeps usernames to one filesystem-safe, unquoted word:
	// letters, digits, dot, underscore and dash. Whitespace, '@', path
	// separators and quotes are rejected, which also means a username can
	// never collide with an e-mail shaped login identifier.
	usernamePattern = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)
	emailPattern    = regexp.MustCompile(`^[^@\s]+@[^@\s]+\.[^@\s]+$`)
	phonePattern    = regexp.MustCompile(`^\+?[0-9]{6,20}$`)
)

// ValidUsername reports whether name satisfies the username rules.
func ValidUsername(name string) bool {
	n := utf8.RuneCountInString(name)
	if n < MinUsernameLen || n > MaxUsernameLen {
		return false
	}
	return usernamePattern.MatchString(name)
}

// ValidPassword reports whether password satisfies the 8-128 byte rule.
func ValidPassword(password string) bool {
	n := len(password)
	return n >= MinPasswordLen && n <= MaxPasswordLen
}

// FoldUsername returns the case-insensitive lookup form of a username.
func FoldUsername(name string) string { return strings.ToLower(strings.TrimSpace(name)) }

// FoldEmail returns the case-insensitive lookup form of an e-mail address.
func FoldEmail(email string) string { return strings.ToLower(strings.TrimSpace(email)) }

// FoldPhone returns the lookup form of a phone number.
func FoldPhone(phone string) string { return strings.TrimSpace(phone) }

// NormalizeEmail trims and validates an optional e-mail address. It returns
// empty strings for an empty input (identifier not provided) and ok=false when
// the input is present but invalid.
func NormalizeEmail(raw string) (email, folded string, ok bool) {
	v := strings.TrimSpace(raw)
	if v == "" {
		return "", "", true
	}
	if len(v) > MaxEmailLen || !emailPattern.MatchString(v) {
		return "", "", false
	}
	return v, strings.ToLower(v), true
}

// NormalizePhone trims and validates an optional phone number. Spaces, dashes
// and parentheses are removed first so a number pasted in human form is
// accepted; everything else must be an optional leading '+' plus 6-20 digits.
func NormalizePhone(raw string) (phone, folded string, ok bool) {
	v := strings.TrimSpace(raw)
	v = phoneTrim.Replace(v)
	if v == "" {
		return "", "", true
	}
	if !phonePattern.MatchString(v) {
		return "", "", false
	}
	return v, v, true
}

var phoneTrim = strings.NewReplacer(" ", "", "\t", "", "-", "", "(", "", ")", "")

// IdentifierKind is the namespace a login identifier is looked up in.
type IdentifierKind int

const (
	// IdentifierUsername is the default: a username handle.
	IdentifierUsername IdentifierKind = iota
	// IdentifierEmail is any identifier containing '@'.
	IdentifierEmail
	// IdentifierPhone is an identifier starting with '+' or made of digits.
	IdentifierPhone
)

// ClassifyIdentifier decides which namespace a login identifier belongs to.
// The rules are deliberately simple and unambiguous (docs/15-m5-plan.md §11):
// '@' means e-mail, a leading '+' or an all-digit string means phone, anything
// else is a username.
func ClassifyIdentifier(raw string) IdentifierKind {
	v := strings.TrimSpace(raw)
	if strings.Contains(v, "@") {
		return IdentifierEmail
	}
	if strings.HasPrefix(v, "+") {
		return IdentifierPhone
	}
	if v != "" && strings.IndexFunc(v, func(r rune) bool { return r < '0' || r > '9' }) < 0 {
		return IdentifierPhone
	}
	return IdentifierUsername
}

// MaskEmail hides the local part of an address for the admin console
// (alice@example.com becomes a***@example.com). Empty stays empty.
func MaskEmail(email string) string {
	if email == "" {
		return ""
	}
	at := strings.LastIndex(email, "@")
	if at <= 0 {
		return "***"
	}
	first, _ := utf8.DecodeRuneInString(email)
	return string(first) + "***" + email[at:]
}

// MaskPhone hides the middle of a phone number for the admin console
// (13800138000 becomes 138****8000). Empty stays empty.
func MaskPhone(phone string) string {
	if phone == "" {
		return ""
	}
	r := []rune(phone)
	if len(r) <= 4 {
		return strings.Repeat("*", len(r))
	}
	head := 3
	for len(r)-head < 4 {
		head--
	}
	if head < 1 {
		head = 1
	}
	return string(r[:head]) + "****" + string(r[len(r)-4:])
}
