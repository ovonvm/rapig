// Package local implements the on-disk, content addressed object store used by
// the local storage policy. Objects are addressed by their sha256 and written
// atomically: bytes go to a temp file first, the hash is computed while
// streaming, and only a fully synced file is renamed into place. A crash can
// therefore leave a temp file behind, never a half written object.
//
// Layout under the configured storage root:
//
//	objects/<aa>/<bb>/<sha256>   immutable object files
//	tmp/                         in-progress uploads (safe to clear on boot)
//
// The two level fan-out keeps directory listings small on ext4/NTFS even with
// many objects.
package local

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// Store is an object store rooted at one directory.
type Store struct {
	root string
}

// New opens (and creates when missing) a store at root.
func New(root string) (*Store, error) {
	if root == "" {
		return nil, errors.New("local: storage root is empty")
	}
	s := &Store{root: root}
	for _, dir := range []string{s.objectsDir(), s.tmpDir()} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("local: create %s: %w", dir, err)
		}
	}
	return s, nil
}

func (s *Store) objectsDir() string { return filepath.Join(s.root, "objects") }
func (s *Store) tmpDir() string     { return filepath.Join(s.root, "tmp") }

// Root returns the configured storage root (absolute path).
func (s *Store) Root() string { return s.root }

// TempDir is the directory for in-flight files. Callers must place partial
// data here (never under objects/) so CleanTemp can always reclaim it.
func (s *Store) TempDir() string { return s.tmpDir() }

// PublishPath moves a completed temp file to its content-addressed home. The
// caller must already know the sha256. When the object exists the temp file is
// removed instead (dedup wins).
func (s *Store) PublishPath(tmpPath, sha256Hex string) error {
	dst, err := s.Path(sha256Hex)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return fmt.Errorf("local: create object shard: %w", err)
	}
	if _, err := os.Stat(dst); err == nil {
		if err := os.Remove(tmpPath); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("local: remove duplicate temp: %w", err)
		}
		return nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("local: stat object: %w", err)
	}
	if err := os.Rename(tmpPath, dst); err != nil {
		return fmt.Errorf("local: publish object: %w", err)
	}
	return nil
}

// Probe verifies the store is writable by round tripping a tiny file. The
// server calls it at startup so a read only or full volume fails fast with a
// clear message instead of failing the first upload in M1.
func (s *Store) Probe(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	f, err := os.CreateTemp(s.tmpDir(), "probe-*")
	if err != nil {
		return fmt.Errorf("local: storage root is not writable: %w", err)
	}
	name := f.Name()
	defer os.Remove(name)
	if _, err := f.WriteString("rapig probe"); err != nil {
		f.Close()
		return fmt.Errorf("local: storage root is not writable: %w", err)
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return fmt.Errorf("local: storage root is not writable: %w", err)
	}
	return f.Close()
}

// Put streams r into the store and returns the sha256 hex of its content plus
// the byte count. Existing content is kept as is: dedup is inherent to the
// address, so a second upload of the same bytes costs no disk space.
func (s *Store) Put(ctx context.Context, r io.Reader) (sha256Hex string, size int64, err error) {
	if err := ctx.Err(); err != nil {
		return "", 0, err
	}
	tmp, err := os.CreateTemp(s.tmpDir(), "put-*")
	if err != nil {
		return "", 0, fmt.Errorf("local: create temp object: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op after a successful rename

	h := sha256.New()
	cw := &countingWriter{w: tmp}
	if _, err := io.Copy(io.MultiWriter(cw, h), r); err != nil {
		tmp.Close()
		return "", 0, fmt.Errorf("local: write object: %w", err)
	}
	if err := ctx.Err(); err != nil {
		tmp.Close()
		return "", 0, err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return "", 0, fmt.Errorf("local: sync object: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return "", 0, fmt.Errorf("local: close object: %w", err)
	}

	sum := hex.EncodeToString(h.Sum(nil))
	final, err := s.Path(sum)
	if err != nil {
		return "", 0, err
	}
	if _, statErr := os.Stat(final); statErr == nil {
		return sum, cw.n, nil // already stored by someone else
	}
	if err := os.MkdirAll(filepath.Dir(final), 0o755); err != nil {
		return "", 0, fmt.Errorf("local: create object shard: %w", err)
	}
	if err := os.Rename(tmpName, final); err != nil {
		return "", 0, fmt.Errorf("local: publish object: %w", err)
	}
	return sum, cw.n, nil
}

// Open returns the stored object for a sha256 hash.
func (s *Store) Open(sha256Hex string) (io.ReadCloser, error) {
	p, err := s.Path(sha256Hex)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(p)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("local: object %s: %w", sha256Hex, fs.ErrNotExist)
		}
		return nil, fmt.Errorf("local: open object %s: %w", sha256Hex, err)
	}
	return f, nil
}

// OpenFile is Open for callers that need Seek (HTTP range serving).
func (s *Store) OpenFile(sha256Hex string) (*os.File, error) {
	p, err := s.Path(sha256Hex)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(p)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("local: object %s: %w", sha256Hex, fs.ErrNotExist)
		}
		return nil, fmt.Errorf("local: open object %s: %w", sha256Hex, err)
	}
	return f, nil
}

// Remove deletes one object. Missing objects are not an error so retries are
// idempotent.
func (s *Store) Remove(sha256Hex string) error {
	p, err := s.Path(sha256Hex)
	if err != nil {
		return err
	}
	if err := os.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("local: remove object %s: %w", sha256Hex, err)
	}
	return nil
}

// Path maps a hash to its absolute file path after validating the hash shape.
func (s *Store) Path(sha256Hex string) (string, error) {
	if !validSHA256(sha256Hex) {
		return "", fmt.Errorf("local: invalid sha256 %q", sha256Hex)
	}
	return filepath.Join(s.objectsDir(), sha256Hex[0:2], sha256Hex[2:4], sha256Hex), nil
}

func validSHA256(s string) bool {
	if len(s) != 64 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// countingWriter counts the bytes written through it.
type countingWriter struct {
	w io.Writer
	n int64
}

func (c *countingWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.n += int64(n)
	return n, err
}

// CleanTemp removes leftover in-progress uploads. Called on boot; safe because
// no upload can be in flight before the server listens.
func (s *Store) CleanTemp() (int, error) {
	entries, err := os.ReadDir(s.tmpDir())
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return 0, nil
		}
		return 0, fmt.Errorf("local: list temp dir: %w", err)
	}
	removed := 0
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasPrefix(name, "put-") && !strings.HasPrefix(name, "probe-") &&
			!strings.HasPrefix(name, "up-") && !strings.HasPrefix(name, "merge-") {
			continue
		}
		if err := os.Remove(filepath.Join(s.tmpDir(), name)); err == nil {
			removed++
		}
	}
	return removed, nil
}
