package files

import (
	"strings"
	"testing"
)

func TestValidateName(t *testing.T) {
	valid := []string{
		"a",
		"测试",
		"my folder",
		"a.b.c",
		"emoji 🐟",
		strings.Repeat("a", MaxNameLength),
		strings.Repeat("测", MaxNameLength),
		" leading-space-is-preserved",
		// Windows device names only reserve the exact base names.
		"console",
		"com10",
		"auxiliary",
	}
	for _, name := range valid {
		if err := ValidateName(name); err != nil {
			t.Errorf("ValidateName(%q) unexpected error: %v", name, err)
		}
	}

	invalid := []string{
		"",
		".",
		"..",
		"a/b",
		`a\b`,
		"ends with dot.",
		"ends with space ",
		"tab\there",
		"newline\nhere",
		"slash/",
		"colon:name",
		"star*name",
		"question?",
		`quote"`,
		"angle<",
		"pipe|",
		strings.Repeat("a", MaxNameLength+1),
		// DOS device names, case insensitive, with or without extension.
		"CON",
		"con.txt",
		"nul",
		"aux.tar.gz",
		"com1.log",
		"LPT9",
		"com0",
		"PrN",
	}
	for _, name := range invalid {
		if err := ValidateName(name); err == nil {
			t.Errorf("ValidateName(%q) = nil, want error", name)
		}
	}
}

func TestFoldNameAndChildPath(t *testing.T) {
	if FoldName("Hello") != "hello" {
		t.Fatalf("FoldName must be case insensitive")
	}
	if FoldName("测试ABC") != "测试abc" {
		t.Fatalf("FoldName must fold ASCII inside unicode")
	}
	if got := ChildPath("", "a"); got != "a" {
		t.Errorf("ChildPath root = %q", got)
	}
	if got := ChildPath("a/b", "c"); got != "a/b/c" {
		t.Errorf("ChildPath nested = %q", got)
	}
}

func TestSanitizeDisplayName(t *testing.T) {
	if got := SanitizeDisplayName("  大鱼  "); got != "大鱼" {
		t.Errorf("SanitizeDisplayName trim = %q", got)
	}
	if got := SanitizeDisplayName("a\x00b\tc"); got != "abc" {
		t.Errorf("SanitizeDisplayName control chars = %q", got)
	}
	long := strings.Repeat("x", 100)
	if got := SanitizeDisplayName(long); len([]rune(got)) != 64 {
		t.Errorf("SanitizeDisplayName length = %d, want 64", len([]rune(got)))
	}
}
