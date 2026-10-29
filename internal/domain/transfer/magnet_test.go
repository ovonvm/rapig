package transfer

import (
	"bytes"
	"encoding/base32"
	"errors"
	"strings"
	"testing"
)

func TestParseMagnetHex(t *testing.T) {
	m, err := ParseMagnet("magnet:?xt=urn:btih:0123456789ABCDEF0123456789ABCDEF01234567&dn=Ubuntu+24.04.iso")
	if err != nil {
		t.Fatalf("ParseMagnet: %v", err)
	}
	if m.Hash != "0123456789abcdef0123456789abcdef01234567" {
		t.Fatalf("hash = %q", m.Hash)
	}
	if m.DisplayName != "Ubuntu 24.04.iso" {
		t.Fatalf("display name = %q", m.DisplayName)
	}
}

func TestParseMagnetBase32(t *testing.T) {
	// 20 bytes of 0x01 encode to 32 base32 characters (RFC 4648, no padding).
	encoded := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(bytes.Repeat([]byte{1}, 20))
	if len(encoded) != 32 {
		t.Fatalf("test vector length = %d", len(encoded))
	}
	m, err := ParseMagnet("magnet:?xt=urn:btih:" + encoded)
	if err != nil {
		t.Fatalf("ParseMagnet: %v", err)
	}
	if m.Hash != strings.Repeat("01", 20) {
		t.Fatalf("hash = %q", m.Hash)
	}
	if m.DisplayName != "" {
		t.Fatalf("display name = %q", m.DisplayName)
	}
}

func TestParseMagnetRejects(t *testing.T) {
	cases := map[string]string{
		"empty":        "",
		"not magnet":   "https://example.com/file.iso",
		"missing xt":   "magnet:?dn=name",
		"wrong urn":    "magnet:?xt=urn:sha1:0123456789abcdef0123456789abcdef01234567",
		"short hex":    "magnet:?xt=urn:btih:0123456789abcdef",
		"bad hex":      "magnet:?xt=urn:btih:zzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzz",
		"bad base32":   "magnet:?xt=urn:btih:11111111111111111111111111111111",
		"too long":     "magnet:?xt=urn:btih:0123456789abcdef0123456789abcdef01234567&dn=" + strings.Repeat("a", MaxMagnetLength),
		"percent junk": "magnet:?xt=urn:btih:0123456789abcdef0123456789abcdef01234567&dn=%zz",
	}
	for name, raw := range cases {
		if _, err := ParseMagnet(raw); err == nil {
			t.Fatalf("%s: expected error", name)
		}
	}
}

func TestParseMagnetTooLongIsDistinct(t *testing.T) {
	_, err := ParseMagnet("magnet:?xt=urn:btih:0123456789abcdef0123456789abcdef01234567&dn=" + strings.Repeat("a", MaxMagnetLength))
	if !errors.Is(err, ErrMagnetTooLong) {
		t.Fatalf("err = %v, want ErrMagnetTooLong", err)
	}
}
