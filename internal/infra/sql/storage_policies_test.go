package sqlstore

import (
	"context"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"rapig/internal/domain/access"
	"rapig/internal/platform/apperr"
	"rapig/migrations"
)

// migrateThrough applies the migrations up to and including last, so a test can
// write pre-M5.3 rows (no enabled/is_default flags, no per-policy blobs) and then
// run the rest of the chain over them.
func migrateThrough(t *testing.T, db *DB, last string) {
	t.Helper()
	names, err := fs.Glob(migrations.FS, "*.sql")
	if err != nil {
		t.Fatalf("glob migrations: %v", err)
	}
	sub := fstest.MapFS{}
	for _, name := range names {
		if name > last {
			continue
		}
		body, err := fs.ReadFile(migrations.FS, name)
		if err != nil {
			t.Fatalf("read migration %s: %v", name, err)
		}
		sub[name] = &fstest.MapFile{Data: body}
	}
	if _, err := db.Migrate(context.Background(), sub); err != nil {
		t.Fatalf("migrate through %s: %v", last, err)
	}
}

func openLegacyDB(t *testing.T) *DB {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	db, err := Open(ctx, filepath.Join(dir, "rapig.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	migrateThrough(t, db, "0012_users_identity.sql")
	return db
}

// TestMigrationBackfillsLegacyStorageRows is the M0–M5.2b compatibility test: a
// database written before multi-storage must come out of 0013 with every policy
// flagged, every node/blob/upload/transfer pointing at the default policy, and
// the old blob id preserved.
func TestMigrationBackfillsLegacyStorageRows(t *testing.T) {
	ctx := context.Background()
	db := openLegacyDB(t)
	now := time.Now().Unix()

	// Pre-M5.3 policy row: no enabled / is_default columns yet.
	res, err := db.ExecContext(ctx,
		`INSERT INTO storage_policies (name, type, root_path, used_bytes, created_at, updated_at)
		 VALUES ('legacy', 'local', 'C:/legacy-root', 0, ?, ?)`, now, now)
	if err != nil {
		t.Fatalf("insert legacy policy: %v", err)
	}
	policyID, err := res.LastInsertId()
	if err != nil {
		t.Fatalf("policy id: %v", err)
	}

	// A blob keyed only by sha256, referenced by one file node.
	res, err = db.ExecContext(ctx,
		`INSERT INTO blobs (sha256, size, refcount, created_at) VALUES (?, 3, 1, ?)`,
		strings.Repeat("a", 64), now)
	if err != nil {
		t.Fatalf("insert legacy blob: %v", err)
	}
	blobID, err := res.LastInsertId()
	if err != nil {
		t.Fatalf("blob id: %v", err)
	}

	userID := seedLegacyUser(t, db, "legacy", "user")
	res, err = db.ExecContext(ctx,
		`INSERT INTO nodes (owner_id, parent_id, kind, name, name_folded, path,
			blob_id, size, storage_policy_id, state, created_at, updated_at)
		 VALUES (?, NULL, 'file', 'old.txt', 'old.txt', 'old.txt', ?, 3, NULL, 'active', ?, ?)`,
		userID, blobID, now, now)
	if err != nil {
		t.Fatalf("insert legacy node: %v", err)
	}
	nodeID, err := res.LastInsertId()
	if err != nil {
		t.Fatalf("node id: %v", err)
	}

	if _, err := db.ExecContext(ctx,
		`INSERT INTO uploads (id, owner_id, parent_id, name, name_folded, size, sha256,
			part_size, parts_total, received_bytes, state, node_id, created_at, expires_at)
		 VALUES ('legacy-upload', ?, ?, 'old.txt', 'old.txt', 3, '',
			1024, 1, 3, 'open', NULL, ?, ?)`,
		userID, nodeID, now, now+3600); err != nil {
		t.Fatalf("insert legacy upload: %v", err)
	}

	if _, err := db.ExecContext(ctx,
		`INSERT INTO transfers (id, owner_id, parent_id, name, name_folded, source_url,
			size, received_bytes, state, error, node_id, created_at, updated_at)
		 VALUES ('legacy-transfer', ?, ?, 'old.txt', 'old.txt', 'http://example.invalid/old.txt',
			3, 3, 'completed', '', NULL, ?, ?)`,
		userID, nodeID, now, now); err != nil {
		t.Fatalf("insert legacy transfer: %v", err)
	}

	if _, err := db.Migrate(ctx, migrations.FS); err != nil {
		t.Fatalf("migrate 0013: %v", err)
	}

	policy, err := db.StoragePolicyByID(ctx, policyID)
	if err != nil {
		t.Fatalf("policy: %v", err)
	}
	if !policy.Enabled || !policy.IsDefault {
		t.Fatalf("policy enabled=%v default=%v, want both true", policy.Enabled, policy.IsDefault)
	}

	var nodePolicy int64
	if err := db.QueryRowContext(ctx,
		`SELECT storage_policy_id FROM nodes WHERE id = ?`, nodeID).Scan(&nodePolicy); err != nil {
		t.Fatalf("node policy: %v", err)
	}
	if nodePolicy != policyID {
		t.Fatalf("node policy = %d, want %d", nodePolicy, policyID)
	}

	blob, err := db.BlobBySHAInPolicy(ctx, policyID, strings.Repeat("a", 64))
	if err != nil {
		t.Fatalf("blob: %v", err)
	}
	if blob.ID != blobID || blob.StoragePolicyID != policyID || blob.Refcount != 1 {
		t.Fatalf("blob id=%d policy=%d refcount=%d, want %d/%d/1",
			blob.ID, blob.StoragePolicyID, blob.Refcount, blobID, policyID)
	}

	var uploadPolicy int64
	if err := db.QueryRowContext(ctx,
		`SELECT storage_policy_id FROM uploads WHERE id = 'legacy-upload'`).Scan(&uploadPolicy); err != nil {
		t.Fatalf("upload policy: %v", err)
	}
	if uploadPolicy != policyID {
		t.Fatalf("upload policy = %d, want %d", uploadPolicy, policyID)
	}

	var transferPolicy int64
	if err := db.QueryRowContext(ctx,
		`SELECT storage_policy_id FROM transfers WHERE id = 'legacy-transfer'`).Scan(&transferPolicy); err != nil {
		t.Fatalf("transfer policy: %v", err)
	}
	if transferPolicy != policyID {
		t.Fatalf("transfer policy = %d, want %d", transferPolicy, policyID)
	}

	// Exactly one default, enforced by the partial unique index.
	var defaults int
	if err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM storage_policies WHERE is_default = 1`).Scan(&defaults); err != nil {
		t.Fatalf("count defaults: %v", err)
	}
	if defaults != 1 {
		t.Fatalf("defaults = %d, want 1", defaults)
	}
}

// TestNodesRequireStoragePolicyTrigger covers the 0013 trigger that stands in
// for a NOT NULL column: a NULL or dangling policy aborts the write on insert
// and on update.
func TestNodesRequireStoragePolicyTrigger(t *testing.T) {
	ctx := context.Background()
	db, _ := newTestDB(t)
	user := seedUser(t, db, "trigger", "user")
	now := time.Now().Unix()

	_, err := db.ExecContext(ctx,
		`INSERT INTO nodes (owner_id, parent_id, kind, name, name_folded, path,
			storage_policy_id, state, created_at, updated_at)
		 VALUES (?, NULL, 'dir', 'bad', 'bad', 'bad', NULL, 'active', ?, ?)`,
		user.ID, now, now)
	if err == nil || !strings.Contains(err.Error(), "storage policy required") {
		t.Fatalf("insert with NULL policy err = %v, want storage policy required", err)
	}

	sc := access.UserScope(user.ID, false)
	root, err := db.EnsureRoot(ctx, user.ID, defaultPolicyID(t, db))
	if err != nil {
		t.Fatalf("root: %v", err)
	}
	dir, err := db.CreateDir(ctx, sc, root.ID, "good", "good")
	if err != nil {
		t.Fatalf("create dir: %v", err)
	}
	if _, err := db.ExecContext(ctx,
		`UPDATE nodes SET storage_policy_id = NULL WHERE id = ?`, dir.ID); err == nil ||
		!strings.Contains(err.Error(), "storage policy required") {
		t.Fatalf("update to NULL policy err = %v, want storage policy required", err)
	}
}

// TestStoragePolicyDefaultsAndUniqueness covers create/list/update/default
// invariants and the usage counters the admin API gates on.
func TestStoragePolicyDefaultsAndUniqueness(t *testing.T) {
	ctx := context.Background()
	db, firstID := newTestDB(t)

	// EnsureDefaultStoragePolicy is idempotent once a default exists.
	again, err := db.EnsureDefaultStoragePolicy(ctx, filepath.Join(t.TempDir(), "ignored"))
	if err != nil {
		t.Fatalf("ensure default: %v", err)
	}
	if again != firstID {
		t.Fatalf("ensure default = %d, want %d", again, firstID)
	}

	secondRoot := filepath.Join(t.TempDir(), "second")
	second, err := db.CreateStoragePolicy(ctx, NewStoragePolicy{
		Name: "second", RootPath: secondRoot, Enabled: true, IsDefault: true,
	})
	if err != nil {
		t.Fatalf("create second: %v", err)
	}
	defaults := countDefaults(t, db)
	if defaults != 1 {
		t.Fatalf("after create-with-default defaults = %d, want 1", defaults)
	}
	first, err := db.StoragePolicyByID(ctx, firstID)
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	if first.IsDefault || !second.IsDefault {
		t.Fatalf("first default=%v second default=%v, want false/true", first.IsDefault, second.IsDefault)
	}

	// Unique name (case sensitive: the admin service trims only).
	if _, err := db.CreateStoragePolicy(ctx, NewStoragePolicy{
		Name: "second", RootPath: filepath.Join(t.TempDir(), "dup"), Enabled: true,
	}); apperr.CodeOf(err) != apperr.CodeNameConflict {
		t.Fatalf("duplicate name err = %v, want name conflict", err)
	}

	// SetDefault moves the flag back and keeps exactly one default.
	if err := db.SetDefaultStoragePolicy(ctx, firstID); err != nil {
		t.Fatalf("set default: %v", err)
	}
	if countDefaults(t, db) != 1 {
		t.Fatal("set default left more than one default")
	}
	first, _ = db.StoragePolicyByID(ctx, firstID)
	second, _ = db.StoragePolicyByID(ctx, second.ID)
	if !first.IsDefault || second.IsDefault {
		t.Fatalf("after set default first=%v second=%v", first.IsDefault, second.IsDefault)
	}

	// Usage counts: one node in the first policy, none in the second.
	user := seedUser(t, db, "counter", "user")
	root, err := db.EnsureRoot(ctx, user.ID, firstID)
	if err != nil {
		t.Fatalf("root: %v", err)
	}
	if _, err := db.CreateDir(ctx, access.UserScope(user.ID, false), root.ID, "docs", "docs"); err != nil {
		t.Fatalf("create dir: %v", err)
	}
	nodes, blobs, err := db.CountStoragePolicyUsage(ctx, firstID)
	if err != nil {
		t.Fatalf("usage: %v", err)
	}
	// The root node lives in the first policy too, so: root + docs.
	if nodes != 2 || blobs != 0 {
		t.Fatalf("usage = %d nodes/%d blobs, want 2/0", nodes, blobs)
	}

	// List with counts (the admin table reads node_count/file_count).
	list, err := db.ListStoragePolicies(ctx, true)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("list len = %d, want 2", len(list))
	}
	for _, p := range list {
		if p.ID == firstID && p.NodeCount != 2 {
			t.Fatalf("first node_count = %d, want 2", p.NodeCount)
		}
	}

	// EnabledStoragePolicies hides disabled roots from the user-facing list.
	if _, err := db.UpdateStoragePolicy(ctx, second.ID, StoragePolicyUpdate{Enabled: boolPtr(false)}); err != nil {
		t.Fatalf("disable second: %v", err)
	}
	enabled, err := db.EnabledStoragePolicies(ctx)
	if err != nil {
		t.Fatalf("enabled list: %v", err)
	}
	for _, p := range enabled {
		if !p.Enabled {
			t.Fatalf("policy %d is disabled but listed", p.ID)
		}
	}
	if len(enabled) != 1 || enabled[0].ID != firstID {
		t.Fatalf("enabled = %+v, want only %d", enabled, firstID)
	}

	if err := db.DeleteStoragePolicy(ctx, second.ID); err != nil {
		t.Fatalf("delete second: %v", err)
	}
	if _, err := db.StoragePolicyByID(ctx, second.ID); apperr.CodeOf(err) != apperr.CodeNotFound {
		t.Fatalf("deleted policy err = %v, want not found", err)
	}
}

// TestBlobsAreUniquePerPolicy pins the M5.3 key: the same content in two roots
// is two rows, and dedup happens inside one policy only.
func TestBlobsAreUniquePerPolicy(t *testing.T) {
	ctx := context.Background()
	db, firstID := newTestDB(t)
	second, err := db.CreateStoragePolicy(ctx, NewStoragePolicy{
		Name: "second", RootPath: filepath.Join(t.TempDir(), "second"), Enabled: true,
	})
	if err != nil {
		t.Fatalf("create policy: %v", err)
	}
	sha := strings.Repeat("b", 64)

	inFirst, created, err := db.InsertBlobInPolicy(ctx, firstID, sha, 10)
	if err != nil || !created {
		t.Fatalf("insert in first created=%v err=%v", created, err)
	}
	inSecond, created, err := db.InsertBlobInPolicy(ctx, second.ID, sha, 10)
	if err != nil || !created {
		t.Fatalf("insert in second created=%v err=%v", created, err)
	}
	if inFirst.ID == inSecond.ID || inSecond.StoragePolicyID != second.ID {
		t.Fatalf("rows = %+v / %+v, want two distinct rows", inFirst, inSecond)
	}

	// Same policy, same content: the existing row comes back, created=false.
	again, created, err := db.InsertBlobInPolicy(ctx, firstID, sha, 10)
	if err != nil {
		t.Fatalf("reinsert: %v", err)
	}
	if created || again.ID != inFirst.ID {
		t.Fatalf("reinsert created=%v id=%d, want false/%d", created, again.ID, inFirst.ID)
	}

	var rows int
	if err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM blobs WHERE sha256 = ?`, sha).Scan(&rows); err != nil {
		t.Fatalf("count blobs: %v", err)
	}
	if rows != 2 {
		t.Fatalf("blob rows for sha = %d, want 2", rows)
	}

	// Refcounts stay per row.
	if err := db.IncRef(ctx, inSecond.ID); err != nil {
		t.Fatalf("inc ref: %v", err)
	}
	reloaded, err := db.BlobByID(ctx, inFirst.ID)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if reloaded.Refcount != 1 {
		t.Fatalf("first refcount = %d, want 1 (second row must not move it)", reloaded.Refcount)
	}
}

func countDefaults(t *testing.T, db *DB) int {
	t.Helper()
	var n int
	if err := db.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM storage_policies WHERE is_default = 1`).Scan(&n); err != nil {
		t.Fatalf("count defaults: %v", err)
	}
	return n
}

func boolPtr(v bool) *bool { return &v }

func defaultPolicyID(t *testing.T, db *DB) int64 {
	t.Helper()
	var id int64
	if err := db.QueryRowContext(context.Background(),
		`SELECT id FROM storage_policies WHERE is_default = 1`).Scan(&id); err != nil {
		t.Fatalf("default policy: %v", err)
	}
	return id
}

// seedLegacyUser inserts a user with raw SQL. openLegacyDB stops at 0012, which
// predates 0014's deleted_at/purge_after columns, so CreateUser (whose scan
// reads those columns) cannot be used to seed a legacy database.
func seedLegacyUser(t *testing.T, db *DB, username, roleCode string) int64 {
	t.Helper()
	ctx := context.Background()
	roleID, err := db.RoleIDByCode(ctx, roleCode)
	if err != nil {
		t.Fatalf("role %q: %v", roleCode, err)
	}
	now := time.Now().Unix()
	res, err := db.ExecContext(ctx,
		`INSERT INTO users (username, username_folded, display_name, password_hash,
			role_id, status, created_at, updated_at)
		 VALUES (?, ?, ?, 'not-a-real-hash', ?, 'active', ?, ?)`,
		username, username, username, roleID, now, now)
	if err != nil {
		t.Fatalf("insert legacy user %q: %v", username, err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		t.Fatalf("legacy user id: %v", err)
	}
	return id
}
