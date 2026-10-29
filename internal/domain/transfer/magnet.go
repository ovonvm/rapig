package transfer

import (
	"encoding/base32"
	"encoding/hex"
	"errors"
	"net/url"
	"strings"
)

const (
	// MaxMagnetLength bounds the stored source URL of a qb job.
	MaxMagnetLength = 2048
	// btihPrefix is the BitTorrent v1 infohash URN prefix.
	btihPrefix = "urn:btih:"
)

var (
	// ErrInvalidMagnet reports a structurally invalid or unsupported magnet
	// link.
	ErrInvalidMagnet = errors.New("invalid magnet link")
	// ErrMagnetTooLong reports a magnet link longer than MaxMagnetLength.
	ErrMagnetTooLong = errors.New("magnet link too long")
)

// Magnet is the parsed subset of a magnet link Rapig supports.
type Magnet struct {
	// Hash is the lowercase hex infohash.
	Hash string
	// DisplayName is the decoded dn parameter, possibly empty.
	DisplayName string
}

// ParseMagnet validates a magnet link and extracts the BitTorrent v1 infohash
// (40 hex or 32 base32 characters) plus the optional display name.
func ParseMagnet(raw string) (Magnet, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return Magnet{}, ErrInvalidMagnet
	}
	if len(raw) > MaxMagnetLength {
		return Magnet{}, ErrMagnetTooLong
	}
	if !strings.HasPrefix(strings.ToLower(raw), "magnet:?") {
		return Magnet{}, ErrInvalidMagnet
	}
	values, err := url.ParseQuery(raw[len("magnet:?"):])
	if err != nil {
		return Magnet{}, ErrInvalidMagnet
	}
	var hash string
	for _, xt := range values["xt"] {
		if !strings.HasPrefix(strings.ToLower(xt), btihPrefix) {
			continue
		}
		h, ok := normalizeBTIH(xt[len(btihPrefix):])
		if !ok {
			return Magnet{}, ErrInvalidMagnet
		}
		hash = h
		break
	}
	if hash == "" {
		return Magnet{}, ErrInvalidMagnet
	}
	return Magnet{Hash: hash, DisplayName: strings.TrimSpace(values.Get("dn"))}, nil
}

// normalizeBTIH converts a 40 hex or 32 base32 infohash to lowercase hex.
func normalizeBTIH(s string) (string, bool) {
	switch len(s) {
	case 40:
		if _, err := hex.DecodeString(s); err != nil {
			return "", false
		}
		return strings.ToLower(s), true
	case 32:
		raw, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(strings.ToUpper(s))
		if err != nil || len(raw) != 20 {
			return "", false
		}
		return hex.EncodeToString(raw), true
	}
	return "", false
}
