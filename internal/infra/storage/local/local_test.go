package local

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newStore(t *testing.T) *Store {
	t.Helper()
	s, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	return s
}

func TestPutOpenRemoveRoundTrip(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	content := []byte("大鱼 rapig object store")

	sum, size, err := s.Put(ctx, bytes.NewReader(content))
	if err != nil {
		t.Fatalf("put: %v", err)
	}
	if size != int64(len(content)) {
		t.Errorf("size = %d, want %d", size, len(content))
	}
	wantSum := sha256.Sum256(content)
	if sum != hex.EncodeToString(wantSum[:]) {
		t.Errorf("sha = %s, want %s", sum, hex.EncodeToString(wantSum[:]))
	}

	p, err := s.Path(sum)
	if err != nil {
		t.Fatalf("path: %v", err)
	}
	if !strings.HasPrefix(p, s.objectsDir()) {
		t.Errorf("object path %q not under %q", p, s.objectsDir())
	}
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("object file missing: %v", err)
	}

	f, err := s.Open(sum)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	got, err := io.ReadAll(f)
	if err != nil || !bytes.Equal(got, content) {
		t.Fatalf("open content = %q, %v", got, err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	if err := s.Remove(sum); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if _, err := s.Open(sum); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("open removed = %v, want fs.ErrNotExist", err)
	}
	// Removing twice is idempotent.
	if err := s.Remove(sum); err != nil {
		t.Fatalf("remove twice: %v", err)
	}
}

func TestPutDeduplicatesContent(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	content := []byte("same bytes twice")
	first, _, err := s.Put(ctx, bytes.NewReader(content))
	if err != nil {
		t.Fatalf("first put: %v", err)
	}
	second, _, err := s.Put(ctx, bytes.NewReader(content))
	if err != nil {
		t.Fatalf("second put: %v", err)
	}
	if first != second {
		t.Fatalf("dedup sha %s vs %s", first, second)
	}
	count := 0
	err = filepath.WalkDir(s.objectsDir(), func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			count++
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk objects: %v", err)
	}
	if count != 1 {
		t.Fatalf("objects after dedup = %d files, want 1", count)
	}
}

func TestPutEmptyContent(t *testing.T) {
	s := newStore(t)
	sum, size, err := s.Put(context.Background(), bytes.NewReader(nil))
	if err != nil {
		t.Fatalf("put empty: %v", err)
	}
	const emptySHA = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
	if sum != emptySHA || size != 0 {
		t.Fatalf("empty put = %q size=%d", sum, size)
	}
}

func TestPathValidation(t *testing.T) {
	s := newStore(t)
	for _, bad := range []string{"", "abc", strings.Repeat("z", 64), strings.Repeat("A", 64), strings.Repeat("a", 63)} {
		if _, err := s.Path(bad); err == nil {
			t.Errorf("Path(%q) = nil, want error", bad)
		}
	}
}

func TestProbeAndCleanTemp(t *testing.T) {
	s := newStore(t)
	if err := s.Probe(context.Background()); err != nil {
		t.Fatalf("probe: %v", err)
	}
	// Simulate a crash: leftover upload files must be removed on boot.
	for _, name := range []string{"put-123", "probe-456"} {
		if err := os.WriteFile(filepath.Join(s.tmpDir(), name), []byte("x"), 0o644); err != nil {
			t.Fatalf("seed temp: %v", err)
		}
	}
	removed, err := s.CleanTemp()
	if err != nil || removed != 2 {
		t.Fatalf("clean temp = %d, %v; want 2", removed, err)
	}
	entries, err := os.ReadDir(s.tmpDir())
	if err != nil || len(entries) != 0 {
		t.Fatalf("temp dir after clean = %d entries, %v", len(entries), err)
	}
}
