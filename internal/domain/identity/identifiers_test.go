package identity

import "testing"

func TestValidUsername(t *testing.T) {
	valid := []string{"ab", "Alice", "a.b_c-d", "123456", repeatRunes('a', MaxUsernameLen)}
	for _, name := range valid {
		if !ValidUsername(name) {
			t.Errorf("ValidUsername(%q) = false, want true", name)
		}
	}
	invalid := []string{"", "a", " a", "a b", "a@b", "a/b", `a"b`, repeatRunes('a', MaxUsernameLen+1)}
	for _, name := range invalid {
		if ValidUsername(name) {
			t.Errorf("ValidUsername(%q) = true, want false", name)
		}
	}
	// 32 runes is the upper bound, 33 is too long.
	if !ValidUsername(repeatRunes('a', MaxUsernameLen)) {
		t.Fatal("32 rune username rejected")
	}
	if ValidUsername(repeatRunes('a', MaxUsernameLen+1)) {
		t.Fatal("33 rune username accepted")
	}
}

func TestValidPassword(t *testing.T) {
	if ValidPassword("1234567") {
		t.Fatal("7 byte password accepted")
	}
	if !ValidPassword("12345678") {
		t.Fatal("8 byte password rejected")
	}
	if !ValidPassword(repeatBytes('a', MaxPasswordLen)) {
		t.Fatal("128 byte password rejected")
	}
	if ValidPassword(repeatBytes('a', MaxPasswordLen+1)) {
		t.Fatal("129 byte password accepted")
	}
}

func TestClassifyIdentifier(t *testing.T) {
	cases := []struct {
		in   string
		want IdentifierKind
	}{
		{"alice", IdentifierUsername},
		{"Alice", IdentifierUsername},
		{"alice@example.com", IdentifierEmail},
		{"no-space@example.com", IdentifierEmail},
		{"13800138000", IdentifierPhone},
		{"+8613800138000", IdentifierPhone},
		{"  +8613800138000 ", IdentifierPhone},
		{"a1", IdentifierUsername},
		{"", IdentifierUsername},
	}
	for _, tc := range cases {
		if got := ClassifyIdentifier(tc.in); got != tc.want {
			t.Errorf("ClassifyIdentifier(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestNormalizeIdentifiers(t *testing.T) {
	email, folded, ok := NormalizeEmail("  Alice@Example.COM ")
	if !ok || email != "Alice@Example.COM" || folded != "alice@example.com" {
		t.Fatalf("NormalizeEmail = %q/%q/%v", email, folded, ok)
	}
	if email, folded, ok := NormalizeEmail("   "); !ok || email != "" || folded != "" {
		t.Fatalf("blank email = %q/%q/%v", email, folded, ok)
	}
	for _, bad := range []string{"alice", "alice@", "@example.com", "alice@example", "a b@example.com"} {
		if _, _, ok := NormalizeEmail(bad); ok {
			t.Errorf("NormalizeEmail(%q) accepted", bad)
		}
	}
	long := repeatBytes('a', MaxEmailLen) + "@example.com"
	if _, _, ok := NormalizeEmail(long); ok {
		t.Error("over long email accepted")
	}

	phone, phoneFolded, ok := NormalizePhone("+86 138-0013-8000")
	if !ok || phone != "+8613800138000" || phoneFolded != "+8613800138000" {
		t.Fatalf("NormalizePhone = %q/%q/%v", phone, phoneFolded, ok)
	}
	if phone, _, ok := NormalizePhone("   "); !ok || phone != "" {
		t.Fatalf("blank phone = %q/%v", phone, ok)
	}
	for _, bad := range []string{"12345", "123456789012345678901", "12ab3456", "+", "++123456"} {
		if _, _, ok := NormalizePhone(bad); ok {
			t.Errorf("NormalizePhone(%q) accepted", bad)
		}
	}
}

func TestMaskEmail(t *testing.T) {
	cases := map[string]string{
		"":                  "",
		"alice@example.com": "a***@example.com",
		"ALICE@example.com": "A***@example.com",
		"no-at-sign":        "***",
	}
	for in, want := range cases {
		if got := MaskEmail(in); got != want {
			t.Errorf("MaskEmail(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestMaskPhone(t *testing.T) {
	cases := map[string]string{
		"":            "",
		"1234":        "****",
		"12345":       "1****2345",
		"13800138000": "138****8000",
		"+8613800138": "+86****0138",
	}
	for in, want := range cases {
		if got := MaskPhone(in); got != want {
			t.Errorf("MaskPhone(%q) = %q, want %q", in, got, want)
		}
	}
	// The mask keeps the value short enough for a table cell but never longer
	// than the original.
	if got := MaskPhone("13800138000"); len(got) != 11 {
		t.Errorf("MaskPhone length = %d, want 11", len(got))
	}
}

func repeatRunes(r rune, n int) string {
	out := make([]rune, n)
	for i := range out {
		out[i] = r
	}
	return string(out)
}

func repeatBytes(b byte, n int) string {
	out := make([]byte, n)
	for i := range out {
		out[i] = b
	}
	return string(out)
}
