package maintenance

import (
	"context"
	"io"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"rapig/internal/domain/access"
	"rapig/internal/domain/identity"
	"rapig/internal/infra/sql"
	storagereg "rapig/internal/infra/storage"
	"rapig/internal/infra/storage/local"
	"rapig/migrations"
)

func newEnv(t *testing.T) (*sqlstore.DB, *local.Store, *storagereg.Registry) {
	t.Helper()
	dir := t.TempDir()
	ctx := context.Background()
	db, err := sqlstore.Open(ctx, dir+"/test.db")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := db.Migrate(ctx, migrations.FS); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if _, err := db.EnsureDefaultStoragePolicy(ctx, dir+"/storage"); err != nil {
		t.Fatalf("policy: %v", err)
	}
	registry := storagereg.NewRegistry(db, noLog())
	if err := registry.ProbeEnabled(ctx); err != nil {
		t.Fatalf("probe: %v", err)
	}
	_, store, err := registry.Default(ctx)
	if err != nil {
		t.Fatalf("default store: %v", err)
	}
	return db, store, registry
}

func noLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func putBlob(t *testing.T, db *sqlstore.DB, store *local.Store, content string) sqlstore.Blob {
	t.Helper()
	sha, size, err := store.Put(context.Background(), strings.NewReader(content))
	if err != nil {
		t.Fatalf("put: %v", err)
	}
	b, _, err := db.InsertBlob(context.Background(), sha, size)
	if err != nil {
		t.Fatalf("insert blob: %v", err)
	}
	return b
}

// reference makes the blob reachable from a node, which is what keeps gc away.
func reference(t *testing.T, db *sqlstore.DB, userID int64, name string, b sqlstore.Blob) {
	t.Helper()
	ctx := context.Background()
	sc := access.UserScope(userID, false)
	root, err := db.EnsureRoot(ctx, userID, mustPolicy(t, db))
	if err != nil {
		t.Fatalf("root: %v", err)
	}
	if _, err := db.CommitCompletedUpload(ctx, sc, sqlstore.CompletedUploadInput{
		ParentID:     root.ID,
		Name:         name,
		Path:         name,
		Mime:         "text/plain",
		SHA256:       b.SHA256,
		Size:         b.Size,
		BlobID:       b.ID,
		IncrementRef: false,
		CreatedAt:    time.Now(),
	}); err != nil {
		t.Fatalf("commit: %v", err)
	}
}

func mustPolicy(t *testing.T, db *sqlstore.DB) int64 {
	t.Helper()
	id, err := db.EnsureDefaultStoragePolicy(context.Background(), t.TempDir())
	if err != nil {
		t.Fatalf("policy: %v", err)
	}
	return id
}

func ensureUser(t *testing.T, db *sqlstore.DB) int64 {
	t.Helper()
	ctx := context.Background()
	roleID, err := db.RoleIDByCode(ctx, "admin")
	if err != nil {
		t.Fatalf("role: %v", err)
	}
	u, err := db.CreateUser(ctx, identity.User{
		Username:       "tester",
		UsernameFolded: "tester",
		DisplayName:    "Tester",
		PasswordHash:   "x",
		RoleID:         roleID,
		Status:         identity.StatusActive,
	})
	if err != nil {
		t.Fatalf("user: %v", err)
	}
	return u.ID
}

func makeOrphan(t *testing.T, db *sqlstore.DB, b sqlstore.Blob, age time.Duration) {
	t.Helper()
	if _, err := db.ExecContext(context.Background(),
		`UPDATE blobs SET created_at = ?, refcount = 0 WHERE id = ?`,
		time.Now().Add(-age).Unix(), b.ID); err != nil {
		t.Fatalf("age blob: %v", err)
	}
}

func TestGCOnlyRemovesAgedOrphans(t *testing.T) {
	ctx := context.Background()
	db, store, registry := newEnv(t)
	live := putBlob(t, db, store, "live content")
	reference(t, db, ensureUser(t, db), "live.txt", live)
	fresh := putBlob(t, db, store, "fresh content")
	old := putBlob(t, db, store, "old content")
	makeOrphan(t, db, old, 48*time.Hour)

	// dry run reports but changes nothing
	res, err := GC(ctx, db, registry, true, time.Now(), noLog())
	if err != nil {
		t.Fatalf("gc dry: %v", err)
	}
	if res.Removed != 1 || !res.DryRun {
		t.Fatalf("dry run removed=%d dry=%v, want 1/true", res.Removed, res.DryRun)
	}
	if _, err := os.Stat(mustPath(t, store, old.SHA256)); err != nil {
		t.Fatalf("dry run deleted the file: %v", err)
	}

	res, err = GC(ctx, db, registry, false, time.Now(), noLog())
	if err != nil {
		t.Fatalf("gc: %v", err)
	}
	if res.Removed != 1 {
		t.Fatalf("removed=%d, want 1", res.Removed)
	}
	if res.BytesFreed != old.Size {
		t.Fatalf("freed=%d, want %d", res.BytesFreed, old.Size)
	}
	if _, err := os.Stat(mustPath(t, store, old.SHA256)); !os.IsNotExist(err) {
		t.Fatalf("old blob file still present: %v", err)
	}
	if _, err := db.BlobBySHA(ctx, old.SHA256); err == nil {
		t.Fatal("old blob row still present")
	}
	// referenced and fresh blobs survive
	if _, err := os.Stat(mustPath(t, store, live.SHA256)); err != nil {
		t.Fatalf("live blob deleted: %v", err)
	}
	if _, err := os.Stat(mustPath(t, store, fresh.SHA256)); err != nil {
		t.Fatalf("fresh blob deleted: %v", err)
	}
}

func TestFsckRepairsRefcountAndFindsDamagedFiles(t *testing.T) {
	ctx := context.Background()
	db, store, registry := newEnv(t)
	good := putBlob(t, db, store, "good content")
	reference(t, db, ensureUser(t, db), "good.txt", good)
	if _, err := db.ExecContext(ctx, `UPDATE blobs SET refcount = 5 WHERE id = ?`, good.ID); err != nil {
		t.Fatalf("tamper refcount: %v", err)
	}

	missing := putBlob(t, db, store, "missing content")
	if err := store.Remove(missing.SHA256); err != nil {
		t.Fatalf("remove file: %v", err)
	}
	truncated := putBlob(t, db, store, "truncated content")
	if err := os.Truncate(mustPath(t, store, truncated.SHA256), 2); err != nil {
		t.Fatalf("truncate: %v", err)
	}

	rep, err := Fsck(ctx, db, registry, false, true, noLog())
	if err != nil {
		t.Fatalf("fsck: %v", err)
	}
	if len(rep.MissingFiles) != 1 || rep.MissingFiles[0] != missing.SHA256 {
		t.Fatalf("missing files = %v, want [%s]", rep.MissingFiles, missing.SHA256)
	}
	if len(rep.SizeMismatches) != 1 || rep.SizeMismatches[0] != truncated.SHA256 {
		t.Fatalf("size mismatches = %v, want [%s]", rep.SizeMismatches, truncated.SHA256)
	}
	if rep.RefcountRepaired != 3 {
		t.Fatalf("refcount repaired = %d, want 3", rep.RefcountRepaired)
	}
	fixed, err := db.BlobBySHA(ctx, good.SHA256)
	if err != nil {
		t.Fatalf("blob: %v", err)
	}
	if fixed.Refcount != 1 {
		t.Fatalf("refcount = %d, want 1", fixed.Refcount)
	}
	if rep.Issues() != 5 {
		t.Fatalf("issues = %d, want 5", rep.Issues())
	}
}

func TestFsckVerifyContent(t *testing.T) {
	ctx := context.Background()
	db, store, registry := newEnv(t)
	b := putBlob(t, db, store, "content to corrupt")
	path := mustPath(t, store, b.SHA256)
	if err := os.WriteFile(path, []byte("different bytes of same length!"), 0o644); err != nil {
		t.Fatalf("write corrupt: %v", err)
	}
	// Make sizes match so only the content check can notice.
	if fi, err := os.Stat(path); err == nil {
		if _, err := db.ExecContext(ctx, `UPDATE blobs SET size = ? WHERE id = ?`, fi.Size(), b.ID); err != nil {
			t.Fatalf("set size: %v", err)
		}
	}
	rep, err := Fsck(ctx, db, registry, true, false, noLog())
	if err != nil {
		t.Fatalf("fsck: %v", err)
	}
	if len(rep.BadContent) != 1 {
		t.Fatalf("bad content = %v, want one entry", rep.BadContent)
	}
}

func mustPath(t *testing.T, store *local.Store, sha string) string {
	t.Helper()
	p, err := store.Path(sha)
	if err != nil {
		t.Fatalf("path: %v", err)
	}
	return p
}
