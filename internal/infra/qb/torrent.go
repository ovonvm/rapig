package qb

// Minimal bencode reader for .torrent files (M6.1b). Rapig only needs the v1
// infohash and the torrent name, so the decoder validates the structure,
// bounds recursion depth and element counts, and records byte spans instead
// of materialising the whole dictionary tree (the `pieces` blob of a real
// torrent is megabytes and would otherwise be copied).

import (
	"bytes"
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"unicode/utf8"
)

// ErrInvalidTorrent reports a .torrent file Rapig cannot parse. Callers
// classify it with errors.Is to answer 400 invalid_torrent.
var ErrInvalidTorrent = errors.New("qb: invalid torrent")

const (
	// torrentMaxDepth bounds container nesting; real torrents stay below 5.
	torrentMaxDepth = 32
	// torrentMaxValues bounds decoded elements so a small "bomb" file cannot
	// burn CPU or memory in the parser.
	torrentMaxValues = 1 << 20
	// errTruncatedText is worded once to keep error strings stable in tests.
	errTruncatedText = "unexpected end of data"
)

var errTruncated = errors.New(errTruncatedText)

// TorrentMeta is the subset of a .torrent file Rapig keeps after an upload.
// The file itself is never persisted: qBittorrent receives the bytes once and
// later polls only need the infohash.
type TorrentMeta struct {
	// InfoHash is the lowercase hex BitTorrent v1 infohash (SHA-1 over the
	// raw bencoded `info` dictionary).
	InfoHash string
	// Name is the `info.name` string, empty when it is absent or not UTF-8.
	Name string
}

// ParseTorrent extracts the infohash and name from a .torrent file. It
// rejects anything that is not exactly one bencoded dictionary holding an
// `info` dictionary.
func ParseTorrent(data []byte) (TorrentMeta, error) {
	if len(data) == 0 || data[0] != 'd' {
		return TorrentMeta{}, fmt.Errorf("%w: top level is not a dictionary", ErrInvalidTorrent)
	}
	d := &bdecoder{buf: data}
	var info []byte
	var infoStart, infoEnd = -1, -1
	err := d.dict(0, func(key []byte, start, end int) error {
		if infoStart >= 0 || !bytes.Equal(key, []byte("info")) {
			return nil
		}
		infoStart, infoEnd = start, end
		return nil
	})
	if err != nil {
		return TorrentMeta{}, fmt.Errorf("%w: %s", ErrInvalidTorrent, err)
	}
	if d.pos != len(data) {
		return TorrentMeta{}, fmt.Errorf("%w: %d trailing bytes", ErrInvalidTorrent, len(data)-d.pos)
	}
	if infoStart < 0 {
		return TorrentMeta{}, fmt.Errorf("%w: missing info dictionary", ErrInvalidTorrent)
	}
	info = data[infoStart:infoEnd]
	if len(info) == 0 || info[0] != 'd' {
		return TorrentMeta{}, fmt.Errorf("%w: info is not a dictionary", ErrInvalidTorrent)
	}
	sum := sha1.Sum(info)
	meta := TorrentMeta{InfoHash: hex.EncodeToString(sum[:])}
	// The name is advisory: a missing, non-string or non-UTF-8 value leaves
	// it empty and the caller falls back to the infohash.
	nd := &bdecoder{buf: info}
	var name []byte
	named := false
	err = nd.dict(0, func(key []byte, start, end int) error {
		if named || !bytes.Equal(key, []byte("name")) {
			return nil
		}
		named = true
		v := &bdecoder{buf: info[start:end]}
		if len(v.buf) == 0 || v.buf[0] < '0' || v.buf[0] > '9' {
			return nil
		}
		_, _, raw, err := v.str()
		if err != nil {
			return nil
		}
		name = raw
		return nil
	})
	if err != nil {
		return TorrentMeta{}, fmt.Errorf("%w: info: %s", ErrInvalidTorrent, err)
	}
	if len(name) > 0 && utf8.Valid(name) {
		meta.Name = string(name)
	}
	return meta, nil
}

// bdecoder walks one bencoded stream. pos is the read cursor; count bounds
// the number of decoded values.
type bdecoder struct {
	buf   []byte
	pos   int
	count int
}

// dict iterates the encoded dictionary at pos and calls fn with each key and
// the span of its value. The whole dictionary is always validated, so a
// truncated or malformed tail is still an error even when fn stops caring.
func (d *bdecoder) dict(depth int, fn func(key []byte, start, end int) error) error {
	if depth > torrentMaxDepth {
		return fmt.Errorf("nesting deeper than %d", torrentMaxDepth)
	}
	if d.pos >= len(d.buf) || d.buf[d.pos] != 'd' {
		return fmt.Errorf("expected dictionary at offset %d", d.pos)
	}
	d.pos++
	for {
		if d.pos >= len(d.buf) {
			return errTruncated
		}
		if d.buf[d.pos] == 'e' {
			d.pos++
			return nil
		}
		_, _, key, err := d.str()
		if err != nil {
			return fmt.Errorf("dictionary key: %w", err)
		}
		start := d.pos
		if err := d.value(depth + 1); err != nil {
			return err
		}
		if fn != nil {
			if err := fn(key, start, d.pos); err != nil {
				return err
			}
		}
	}
}

// value parses exactly one bencoded value and advances past it.
func (d *bdecoder) value(depth int) error {
	if depth > torrentMaxDepth {
		return fmt.Errorf("nesting deeper than %d", torrentMaxDepth)
	}
	d.count++
	if d.count > torrentMaxValues {
		return fmt.Errorf("more than %d values", torrentMaxValues)
	}
	if d.pos >= len(d.buf) {
		return errTruncated
	}
	switch c := d.buf[d.pos]; {
	case c == 'i':
		_, err := d.integer()
		return err
	case c == 'l':
		d.pos++
		for {
			if d.pos >= len(d.buf) {
				return errTruncated
			}
			if d.buf[d.pos] == 'e' {
				d.pos++
				return nil
			}
			if err := d.value(depth + 1); err != nil {
				return err
			}
		}
	case c == 'd':
		return d.dict(depth, nil)
	case c >= '0' && c <= '9':
		_, _, _, err := d.str()
		return err
	default:
		return fmt.Errorf("unexpected byte %q at offset %d", c, d.pos)
	}
}

// str parses a byte string at pos and returns its encoded span plus the
// decoded bytes (aliasing buf, never copied).
func (d *bdecoder) str() (start, end int, val []byte, err error) {
	start = d.pos
	colon := bytes.IndexByte(d.buf[d.pos:], ':')
	if colon < 0 {
		return 0, 0, nil, errTruncated
	}
	// A length field longer than 18 digits cannot describe a body that fits
	// in memory, and a leading zero is invalid bencode.
	field := d.buf[d.pos : d.pos+colon]
	if len(field) == 0 || len(field) > 18 {
		return 0, 0, nil, fmt.Errorf("invalid string length %q", truncate(string(field)))
	}
	for _, b := range field {
		if b < '0' || b > '9' {
			return 0, 0, nil, fmt.Errorf("invalid string length %q", truncate(string(field)))
		}
	}
	if len(field) > 1 && field[0] == '0' {
		return 0, 0, nil, fmt.Errorf("string length %q has a leading zero", truncate(string(field)))
	}
	n, convErr := strconv.Atoi(string(field))
	if convErr != nil {
		return 0, 0, nil, fmt.Errorf("invalid string length %q", truncate(string(field)))
	}
	body := d.pos + colon + 1
	if n > len(d.buf)-body {
		return 0, 0, nil, errTruncated
	}
	d.pos = body + n
	return start, d.pos, d.buf[body:d.pos], nil
}

// integer parses i<digits>e, rejecting empty, leading-zero and non-numeric
// forms.
func (d *bdecoder) integer() (int64, error) {
	d.pos++ // 'i'
	start := d.pos
	for d.pos < len(d.buf) && d.buf[d.pos] != 'e' {
		d.pos++
	}
	if d.pos >= len(d.buf) {
		return 0, errTruncated
	}
	field := d.buf[start:d.pos]
	d.pos++ // 'e'
	digits := field
	if len(digits) > 0 && digits[0] == '-' {
		digits = digits[1:]
	}
	if len(digits) == 0 {
		return 0, errors.New("integer without digits")
	}
	for _, b := range digits {
		if b < '0' || b > '9' {
			return 0, fmt.Errorf("invalid integer %q", truncate(string(field)))
		}
	}
	if len(digits) > 1 && digits[0] == '0' {
		return 0, fmt.Errorf("integer %q has a leading zero", truncate(string(field)))
	}
	if len(field) > 0 && field[0] == '-' && digits[0] == '0' {
		return 0, errors.New("negative zero")
	}
	n, convErr := strconv.ParseInt(string(field), 10, 64)
	if convErr != nil {
		return 0, fmt.Errorf("invalid integer %q", truncate(string(field)))
	}
	return n, nil
}
