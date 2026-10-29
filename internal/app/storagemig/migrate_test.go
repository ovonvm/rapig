package storagemig_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"rapig/internal/app/storagemig"
	sqlstore "rapig/internal/infra/sql"
	"rapig/internal/infra/storage"
	"rapig/internal/infra/storage/local"
	"rapig/migrations"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

type env struct {
	ctx     context.Context
	db      *sqlstore.DB
	reg     *storage.Registry
	fromID  int64
	toID    int64
	rootA   string
	rootB   string
	userID  int64
	rootDir int64
}

func newEnv(t *testing.T) *env {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	db, err := sqlstore.OpenWithOptions(ctx, filepath.Join(dir, "db.sqlite"), sqlstore.DefaultOptions())
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := db.Migrate(ctx, migrations.FS); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	rootA := filepath.Join(dir, "root-a")
	rootB := filepath.Join(dir, "root-b")
	for _, r := range []string{rootA, rootB} {
		if err := os.MkdirAll(r, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	fromID, err := db.EnsureDefaultStoragePolicy(ctx, rootA)
	if err != nil {
		t.Fatalf("default policy: %v", err)
	}
	target, err := db.CreateStoragePolicy(ctx, sqlstore.NewStoragePolicy{Name: "target", RootPath: rootB, Enabled: true})
	if err != nil {
		t.Fatalf("create policy: %v", err)
	}
	log := testLogger()
	e := &env{ctx: ctx, db: db, reg: storage.NewRegistry(db, log), fromID: fromID, toID: target.ID, rootA: rootA, rootB: rootB}

	now := time.Now().Unix()
	var roleID int64
	if err := db.QueryRowContext(ctx, `SELECT id FROM roles WHERE code = 'user'`).Scan(&roleID); err != nil {
		t.Fatalf("role: %v", err)
	}
	res, err := db.ExecContext(ctx,
		`INSERT INTO users (username, username_folded, display_name, password_hash, role_id, status, used_bytes, quota_bytes, created_at, updated_at)
		 VALUES ('u1', 'u1', 'u1', 'x', ?, 'active', 0, 0, ?, ?)`, roleID, now, now)
	if err != nil {
		t.Fatalf("insert user: %v", err)
	}
	e.userID, _ = res.LastInsertId()
	res, err = db.ExecContext(ctx,
		`INSERT INTO nodes (owner_id, parent_id, kind, name, name_folded, path, blob_id, size, storage_policy_id, state, created_at, updated_at)
		 VALUES (?, NULL, 'dir', 'root', 'root', '', NULL, 0, ?, 'active', ?, ?)`, e.userID, e.fromID, now, now)
	if err != nil {
		t.Fatalf("root node: %v", err)
	}
	e.rootDir, _ = res.LastInsertId()
	return e
}

// disableSource writes the enabled flag directly: UpdateStoragePolicy refuses
// to disable a policy that still has nodes, which is the normal migration
// starting state.
func (e *env) disableSource(t *testing.T) {
	t.Helper()
	if _, err := e.db.ExecContext(e.ctx, `UPDATE storage_policies SET enabled = 0 WHERE id = ?`, e.fromID); err != nil {
		t.Fatalf("disable source: %v", err)
	}
}

func (e *env) addFile(t *testing.T, name string, data []byte) int64 {
	t.Helper()
	store, err := local.New(e.rootA)
	if err != nil {
		t.Fatalf("open rootA: %v", err)
	}
	sha, size, err := store.Put(e.ctx, bytes.NewReader(data))
	if err != nil {
		t.Fatalf("store object: %v", err)
	}
	blob, _, err := e.db.InsertBlobInPolicy(e.ctx, e.fromID, sha, size)
	if err != nil {
		t.Fatalf("insert blob: %v", err)
	}
	now := time.Now().Unix()
	res, err := e.db.ExecContext(e.ctx,
		`INSERT INTO nodes (owner_id, parent_id, kind, name, name_folded, path, blob_id, size, storage_policy_id, state, created_at, updated_at)
		 VALUES (?, ?, 'file', ?, ?, '', ?, ?, ?, 'active', ?, ?)`,
		e.userID, e.rootDir, name, name, blob.ID, size, e.fromID, now, now)
	if err != nil {
		t.Fatalf("insert file node: %v", err)
	}
	id, _ := res.LastInsertId()
	return id
}

func (e *env) blobCount(t *testing.T, policyID int64) int {
	t.Helper()
	var n int
	if err := e.db.QueryRowContext(e.ctx, `SELECT COUNT(*) FROM blobs WHERE storage_policy_id = ?`, policyID).Scan(&n); err != nil {
		t.Fatalf("count blobs: %v", err)
	}
	return n
}

func TestMigrateMovesBlobsAndNodes(t *testing.T) {
	e := newEnv(t)
	e.disableSource(t)
	e.addFile(t, "a.txt", []byte("hello migration"))

	rep, err := storagemig.Migrate(e.ctx, e.db, e.reg, testLogger(), storagemig.Options{From: e.fromID, To: e.toID})
	if err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if rep.Blobs != 1 || rep.Nodes != 1 || rep.Copied != 1 || rep.RemovedFiles != 1 {
		t.Fatalf("report = %+v", rep)
	}
	if n := e.blobCount(t, e.fromID); n != 0 {
		t.Errorf("source policy blobs = %d, want 0", n)
	}
	if n := e.blobCount(t, e.toID); n != 1 {
		t.Errorf("target policy blobs = %d, want 1", n)
	}
	var policyID, blobID int64
	if err := e.db.QueryRowContext(e.ctx,
		`SELECT storage_policy_id, blob_id FROM nodes WHERE kind = 'file'`).Scan(&policyID, &blobID); err != nil {
		t.Fatalf("load node: %v", err)
	}
	if policyID != e.toID || blobID == 0 {
		t.Errorf("node policy/blob = %d/%d, want %d/nonzero", policyID, blobID, e.toID)
	}
	// blob_id must point at the target policy row, not the deleted source row.
	var targetPolicy int64
	if err := e.db.QueryRowContext(e.ctx, `SELECT storage_policy_id FROM blobs WHERE id = ?`, blobID).Scan(&targetPolicy); err != nil {
		t.Fatalf("load blob: %v", err)
	}
	if targetPolicy != e.toID {
		t.Errorf("node.blob_id points at policy %d, want %d", targetPolicy, e.toID)
	}
	// Rerunning an interrupted/finished migration must be a no-op.
	rep, err = storagemig.Migrate(e.ctx, e.db, e.reg, testLogger(), storagemig.Options{From: e.fromID, To: e.toID})
	if err != nil {
		t.Fatalf("second migrate: %v", err)
	}
	if rep.Blobs != 0 {
		t.Errorf("second run blobs = %d, want 0", rep.Blobs)
	}
}

func TestDryRunDoesNotChangeState(t *testing.T) {
	e := newEnv(t)
	e.disableSource(t)
	e.addFile(t, "a.txt", []byte("dry run"))

	rep, err := storagemig.Migrate(e.ctx, e.db, e.reg, testLogger(), storagemig.Options{From: e.fromID, To: e.toID, DryRun: true})
	if err != nil {
		t.Fatalf("dry run: %v", err)
	}
	if !rep.DryRun || rep.Blobs != 1 || rep.Nodes != 1 {
		t.Fatalf("report = %+v", rep)
	}
	if e.blobCount(t, e.fromID) != 1 || e.blobCount(t, e.toID) != 0 {
		t.Errorf("dry run changed blob rows")
	}
	if _, err := os.Stat(filepath.Join(e.rootA, "objects")); err != nil {
		t.Errorf("source objects dir missing: %v", err)
	}
}

func TestRejectsInvalidPolicyCombinations(t *testing.T) {
	e := newEnv(t)

	if _, err := storagemig.Migrate(e.ctx, e.db, e.reg, testLogger(), storagemig.Options{From: e.fromID, To: e.fromID}); !errors.Is(err, storagemig.ErrSamePolicy) {
		t.Errorf("same policy error = %v", err)
	}
	if _, err := storagemig.Migrate(e.ctx, e.db, e.reg, testLogger(), storagemig.Options{From: e.fromID, To: e.toID}); !errors.Is(err, storagemig.ErrSourceEnabled) {
		t.Errorf("enabled source error = %v", err)
	}
	e.disableSource(t)
	if _, err := storagemig.Migrate(e.ctx, e.db, e.reg, testLogger(), storagemig.Options{From: e.fromID, To: e.fromID}); err == nil {
		t.Errorf("expected error for same policy after disable")
	}
	if _, err := storagemig.Migrate(e.ctx, e.db, e.reg, testLogger(), storagemig.Options{From: e.toID, To: e.toID}); err == nil {
		t.Errorf("expected error when target is the same policy")
	}
}

func TestRejectsRefcountDrift(t *testing.T) {
	e := newEnv(t)
	e.disableSource(t)
	e.addFile(t, "a.txt", []byte("drift"))
	if _, err := e.db.ExecContext(e.ctx, `UPDATE blobs SET refcount = refcount + 1`); err != nil {
		t.Fatalf("bump refcount: %v", err)
	}
	if _, err := storagemig.Migrate(e.ctx, e.db, e.reg, testLogger(), storagemig.Options{From: e.fromID, To: e.toID}); !errors.Is(err, storagemig.ErrRefcountDrift) {
		t.Errorf("drift error = %v", err)
	}
}

func TestResolvePolicy(t *testing.T) {
	e := newEnv(t)
	id, err := storagemig.ResolvePolicy(e.ctx, e.db, "target")
	if err != nil || id != e.toID {
		t.Fatalf("resolve by name = %d, %v", id, err)
	}
	id, err = storagemig.ResolvePolicy(e.ctx, e.db, "#"+strconv.FormatInt(e.toID, 10))
	if err != nil || id != e.toID {
		t.Fatalf("resolve by id = %d, %v", id, err)
	}
	if _, err := storagemig.ResolvePolicy(e.ctx, e.db, "missing"); err == nil {
		t.Errorf("expected error for unknown policy")
	}
}
