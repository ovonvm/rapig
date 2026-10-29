package sqlstore

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"rapig/internal/domain/access"
	domainidentity "rapig/internal/domain/identity"
	"rapig/internal/platform/apperr"
	"rapig/migrations"
)

func newTestDB(t *testing.T) (*DB, int64) {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	db, err := Open(ctx, filepath.Join(dir, "rapig.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Migrate(ctx, migrations.FS); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	policyID, err := db.EnsureDefaultStoragePolicy(ctx, filepath.Join(dir, "storage"))
	if err != nil {
		t.Fatalf("storage policy: %v", err)
	}
	return db, policyID
}

func seedUser(t *testing.T, db *DB, username, roleCode string) domainidentity.User {
	t.Helper()
	ctx := context.Background()
	roleID, err := db.RoleIDByCode(ctx, roleCode)
	if err != nil {
		t.Fatalf("role %q: %v", roleCode, err)
	}
	u, err := db.CreateUser(ctx, domainidentity.User{
		Username:       username,
		UsernameFolded: username,
		DisplayName:    username,
		PasswordHash:   "not-a-real-hash",
		RoleID:         roleID,
		Status:         domainidentity.StatusActive,
	})
	if err != nil {
		t.Fatalf("create user %q: %v", username, err)
	}
	return u
}

func TestScopesAreRequired(t *testing.T) {
	db, _ := newTestDB(t)
	ctx := context.Background()
	u := seedUser(t, db, "alice", "user")
	empty := access.Scope{}

	_, err := db.RootNode(ctx, empty)
	if apperr.CodeOf(err) != apperr.CodeInvalidScope {
		t.Errorf("RootNode(empty scope) = %v, want invalid_scope", err)
	}
	_, err = db.NodeByID(ctx, empty, 1)
	if apperr.CodeOf(err) != apperr.CodeInvalidScope {
		t.Errorf("NodeByID(empty scope) = %v, want invalid_scope", err)
	}
	_, _, err = db.ListChildren(ctx, empty, 1, 10, 0)
	if apperr.CodeOf(err) != apperr.CodeInvalidScope {
		t.Errorf("ListChildren(empty scope) = %v, want invalid_scope", err)
	}
	_, err = db.CreateDir(ctx, empty, 1, "x", "x")
	if apperr.CodeOf(err) != apperr.CodeInvalidScope {
		t.Errorf("CreateDir(empty scope) = %v, want invalid_scope", err)
	}
	_ = u
}

func TestRootIsIdempotentAndOwnerScoped(t *testing.T) {
	db, policyID := newTestDB(t)
	ctx := context.Background()
	alice := seedUser(t, db, "alice", "user")
	bob := seedUser(t, db, "bob", "user")

	rootA, err := db.EnsureRoot(ctx, alice.ID, policyID)
	if err != nil {
		t.Fatalf("ensure root alice: %v", err)
	}
	rootA2, err := db.EnsureRoot(ctx, alice.ID, policyID)
	if err != nil {
		t.Fatalf("ensure root alice again: %v", err)
	}
	if rootA.ID != rootA2.ID {
		t.Errorf("EnsureRoot not idempotent: %d vs %d", rootA.ID, rootA2.ID)
	}
	if !rootA.IsRoot() {
		t.Errorf("root node should have nil parent")
	}

	rootB, err := db.EnsureRoot(ctx, bob.ID, policyID)
	if err != nil {
		t.Fatalf("ensure root bob: %v", err)
	}
	if rootA.ID == rootB.ID {
		t.Fatalf("two owners share a root node id %d", rootA.ID)
	}

	gotA, err := db.RootNode(ctx, access.UserScope(alice.ID, false))
	if err != nil || gotA.ID != rootA.ID {
		t.Fatalf("RootNode(alice) = %+v, %v; want %d", gotA, err, rootA.ID)
	}
	gotB, err := db.RootNode(ctx, access.UserScope(bob.ID, false))
	if err != nil || gotB.ID != rootB.ID {
		t.Fatalf("RootNode(bob) = %+v, %v; want %d", gotB, err, rootB.ID)
	}
}

func TestCreateDirAndListChildren(t *testing.T) {
	db, policyID := newTestDB(t)
	ctx := context.Background()
	alice := seedUser(t, db, "alice", "user")
	sc := access.UserScope(alice.ID, false)
	root, err := db.EnsureRoot(ctx, alice.ID, policyID)
	if err != nil {
		t.Fatalf("root: %v", err)
	}

	dir, err := db.CreateDir(ctx, sc, root.ID, "相册", "相册")
	if err != nil {
		t.Fatalf("create dir: %v", err)
	}
	if dir.Name != "相册" || dir.Path != "相册" || dir.ParentID == nil || *dir.ParentID != root.ID {
		t.Fatalf("unexpected node: %+v", dir)
	}

	child, err := db.CreateDir(ctx, sc, dir.ID, "2026", "相册/2026")
	if err != nil {
		t.Fatalf("create child: %v", err)
	}
	if child.Path != "相册/2026" {
		t.Errorf("child path = %q", child.Path)
	}

	if _, err := db.CreateDir(ctx, sc, root.ID, "相册", "相册"); apperr.CodeOf(err) != apperr.CodeNameConflict {
		t.Errorf("duplicate dir = %v, want name_conflict", err)
	}
	// Case folded uniqueness: "相册" vs "相册" only; use ascii pair for the
	// case-insensitive rule.
	if _, err := db.CreateDir(ctx, sc, root.ID, "Photos", "Photos"); err != nil {
		t.Fatalf("create Photos: %v", err)
	}
	if _, err := db.CreateDir(ctx, sc, root.ID, "photos", "photos"); apperr.CodeOf(err) != apperr.CodeNameConflict {
		t.Errorf("case folded duplicate = %v, want name_conflict", err)
	}
	if _, err := db.CreateDir(ctx, sc, 999_999, "orphan", "orphan"); apperr.CodeOf(err) != apperr.CodeParentNotFound {
		t.Errorf("missing parent = %v, want parent_not_found", err)
	}

	items, total, err := db.ListChildren(ctx, sc, root.ID, 50, 0)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if total != 2 || len(items) != 2 {
		t.Fatalf("list = %d items (total %d), want 2", len(items), total)
	}
	// Directories first, then case folded name.
	if items[0].Name != "Photos" || items[1].Name != "相册" {
		t.Errorf("unexpected order: %q, %q", items[0].Name, items[1].Name)
	}

	page, pageTotal, err := db.ListChildren(ctx, sc, root.ID, 1, 0)
	if err != nil || pageTotal != 2 || len(page) != 1 {
		t.Fatalf("paged list = %d/%d, %v", len(page), pageTotal, err)
	}
}

func TestForeignNodesAreInvisible(t *testing.T) {
	db, policyID := newTestDB(t)
	ctx := context.Background()
	alice := seedUser(t, db, "alice", "user")
	bob := seedUser(t, db, "bob", "user")

	bobRoot, err := db.EnsureRoot(ctx, bob.ID, policyID)
	if err != nil {
		t.Fatalf("bob root: %v", err)
	}
	bobDir, err := db.CreateDir(ctx, access.UserScope(bob.ID, false), bobRoot.ID, "private", "private")
	if err != nil {
		t.Fatalf("bob dir: %v", err)
	}

	aliceScope := access.UserScope(alice.ID, false)
	if _, err := db.NodeByID(ctx, aliceScope, bobDir.ID); apperr.CodeOf(err) != apperr.CodeNotFound {
		t.Errorf("NodeByID(foreign) = %v, want not_found", err)
	}
	if _, err := db.NodeByID(ctx, aliceScope, bobRoot.ID); apperr.CodeOf(err) != apperr.CodeNotFound {
		t.Errorf("NodeByID(foreign root) = %v, want not_found", err)
	}
	items, total, err := db.ListChildren(ctx, aliceScope, bobRoot.ID, 50, 0)
	if err != nil {
		t.Fatalf("ListChildren(foreign) errored: %v", err)
	}
	if total != 0 || len(items) != 0 {
		t.Errorf("ListChildren(foreign) leaked %d nodes", total)
	}
}

func TestMigrateChecksumDetectsChangedMigration(t *testing.T) {
	ctx := context.Background()
	db, err := Open(ctx, filepath.Join(t.TempDir(), "checksum.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	fsys := fstest.MapFS{
		"0001_probe.sql": &fstest.MapFile{Data: []byte("CREATE TABLE probe (id INTEGER PRIMARY KEY);")},
	}
	applied, err := db.Migrate(ctx, fsys)
	if err != nil || len(applied) != 1 {
		t.Fatalf("first migrate = %v, %v", applied, err)
	}
	// Unchanged file: nothing to apply and no error.
	applied, err = db.Migrate(ctx, fsys)
	if err != nil || len(applied) != 0 {
		t.Fatalf("second migrate = %v, %v", applied, err)
	}
	// Changed file: must be refused, not silently skipped.
	fsys["0001_probe.sql"] = &fstest.MapFile{Data: []byte("CREATE TABLE probe (id INTEGER PRIMARY KEY, x TEXT);")}
	if _, err := db.Migrate(ctx, fsys); err == nil || !strings.Contains(err.Error(), "changed after it was applied") {
		t.Fatalf("changed migration err = %v, want checksum mismatch", err)
	}
}

func TestBlobRefcountLifecycle(t *testing.T) {
	db, _ := newTestDB(t)
	ctx := context.Background()
	sha := strings.Repeat("ab", 32)

	first, created, err := db.InsertBlob(ctx, sha, 1234)
	if err != nil {
		t.Fatalf("insert blob: %v", err)
	}
	if !created || first.Refcount != 1 || first.Size != 1234 {
		t.Fatalf("first insert = %+v created=%v", first, created)
	}

	// Same content again: dedup, the existing row is returned untouched.
	second, createdAgain, err := db.InsertBlob(ctx, sha, 1234)
	if err != nil {
		t.Fatalf("second insert: %v", err)
	}
	if createdAgain || second.ID != first.ID || second.Refcount != 1 {
		t.Fatalf("dedup insert = %+v created=%v", second, createdAgain)
	}

	if err := db.IncRef(ctx, first.ID); err != nil {
		t.Fatalf("inc ref: %v", err)
	}
	got, err := db.BlobBySHA(ctx, sha)
	if err != nil || got.Refcount != 2 {
		t.Fatalf("after inc = %+v, %v", got, err)
	}

	remaining, err := db.DecRef(ctx, first.ID)
	if err != nil || remaining != 1 {
		t.Fatalf("first dec = %d, %v", remaining, err)
	}
	remaining, err = db.DecRef(ctx, first.ID)
	if err != nil || remaining != 0 {
		t.Fatalf("second dec = %d, %v", remaining, err)
	}
	if _, err := db.BlobBySHA(ctx, sha); apperr.CodeOf(err) != apperr.CodeNotFound {
		t.Fatalf("deleted blob = %v, want not_found", err)
	}
	if _, err := db.DecRef(ctx, first.ID); apperr.CodeOf(err) != apperr.CodeNotFound {
		t.Fatalf("dec deleted blob = %v, want not_found", err)
	}
}

func TestSettingsRoundTrip(t *testing.T) {
	db, _ := newTestDB(t)
	ctx := context.Background()

	if _, ok, err := db.Setting(ctx, "missing"); err != nil || ok {
		t.Fatalf("missing setting = ok=%v err=%v", ok, err)
	}
	if err := db.SetSetting(ctx, "audit_retention_days", "30"); err != nil {
		t.Fatalf("set setting: %v", err)
	}
	if v, ok, err := db.Setting(ctx, "audit_retention_days"); err != nil || !ok || v != "30" {
		t.Fatalf("get setting = %q ok=%v err=%v", v, ok, err)
	}
	if err := db.SetSetting(ctx, "audit_retention_days", "7"); err != nil {
		t.Fatalf("update setting: %v", err)
	}
	if v, _, _ := db.Setting(ctx, "audit_retention_days"); v != "7" {
		t.Fatalf("updated setting = %q, want 7", v)
	}
	all, err := db.AllSettings(ctx)
	if err != nil {
		t.Fatalf("all settings: %v", err)
	}
	if all["audit_retention_days"] != "7" {
		t.Fatalf("all settings = %v, %v", all, err)
	}
	// Migration 0012 seeds the registration default quota at 1 GiB.
	if all[SettingDefaultQuotaBytes] != "1073741824" {
		t.Fatalf("default quota = %q, want 1073741824", all[SettingDefaultQuotaBytes])
	}
	// Migration 0014 seeds the self-service deletion switch closed.
	if all[SettingDeletionEnabled] != "0" {
		t.Fatalf("deletion_enabled = %q, want 0", all[SettingDeletionEnabled])
	}
	if len(all) != 3 {
		t.Fatalf("unexpected settings: %v", all)
	}
}

func TestPruneAudit(t *testing.T) {
	db, _ := newTestDB(t)
	ctx := context.Background()
	old := time.Now().Add(-48 * time.Hour)
	fresh := time.Now().Add(-time.Hour)
	for _, at := range []time.Time{old, fresh} {
		if err := db.InsertAudit(ctx, AuditEvent{At: at, Action: "test.event", Result: "ok"}); err != nil {
			t.Fatalf("insert audit: %v", err)
		}
	}
	removed, err := db.PruneAudit(ctx, time.Now().Add(-24*time.Hour))
	if err != nil || removed != 1 {
		t.Fatalf("prune = %d, %v; want 1", removed, err)
	}
	var count int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM audit`).Scan(&count); err != nil {
		t.Fatalf("count audit: %v", err)
	}
	if count != 1 {
		t.Fatalf("audit rows after prune = %d, want 1", count)
	}
}
