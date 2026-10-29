// Package idgen generates opaque random identifiers and tokens.
package idgen

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
)

// Bytes returns n cryptographically random bytes.
func Bytes(n int) ([]byte, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return nil, fmt.Errorf("idgen: read random bytes: %w", err)
	}
	return b, nil
}

// Token returns n random bytes encoded as unpadded base64url, and therefore
// at least n bytes of entropy.
func Token(n int) (string, error) {
	b, err := Bytes(n)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// MustToken is Token that panics on failure; only for startup paths where a
// broken CSPRNG means the process must not continue.
func MustToken(n int) string {
	t, err := Token(n)
	if err != nil {
		panic(err)
	}
	return t
}

// lowerAlnum is the alphabet used for generated handles: lowercase letters and
// digits, so an auto-generated username never needs quoting anywhere.
const lowerAlnum = "abcdefghijklmnopqrstuvwxyz0123456789"

// LowerAlnum returns n random lowercase letters or digits. Each byte is drawn
// with crypto/rand and bytes above the largest multiple of the alphabet size
// are rejected, so the distribution stays uniform.
func LowerAlnum(n int) (string, error) {
	if n <= 0 {
		return "", nil
	}
	ceiling := byte(256 - 256%len(lowerAlnum))
	out := make([]byte, 0, n)
	b := make([]byte, 1)
	for len(out) < n {
		if _, err := rand.Read(b); err != nil {
			return "", fmt.Errorf("idgen: read random bytes: %w", err)
		}
		if b[0] >= ceiling {
			continue
		}
		out = append(out, lowerAlnum[int(b[0])%len(lowerAlnum)])
	}
	return string(out), nil
}

// MustLowerAlnum is LowerAlnum that panics on failure; only for paths where a
// broken CSPRNG means the request must not continue.
func MustLowerAlnum(n int) string {
	s, err := LowerAlnum(n)
	if err != nil {
		panic(err)
	}
	return s
}

// RequestID returns a 16 character hex identifier for request correlation.
func RequestID() string {
	b, err := Bytes(8)
	if err != nil {
		return "0000000000000000"
	}
	return hex.EncodeToString(b)
}

// SHA256Hex returns the lowercase hex encoded SHA-256 of s.
func SHA256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}
