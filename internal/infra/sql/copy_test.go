package sqlstore

import (
	"context"
	"testing"
	"time"

	"rapig/internal/domain/access"
	"rapig/internal/domain/files"
	"rapig/internal/platform/apperr"
)

// TestCopyNodeSubtree duplicates a directory tree, shares blobs and charges
// quota without touching the source.
func TestCopyNodeSubtree(t *testing.T) {
	ctx := context.Background()
	db, policyID := newTestDB(t)
	u := seedUser(t, db, "alice", "user")
	sc := access.UserScope(u.ID, false)
	root, err := db.EnsureRoot(ctx, u.ID, policyID)
	if err != nil {
		t.Fatalf("root: %v", err)
	}
	dir, err := db.CreateDir(ctx, sc, root.ID, "相册", files.ChildPath(root.Path, "相册"))
	if err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	sub, err := db.CreateDir(ctx, sc, dir.ID, "2024", files.ChildPath(dir.Path, "2024"))
	if err != nil {
		t.Fatalf("mkdir sub: %v", err)
	}
	_ = mkFile(t, db, sc, dir, "a.txt", "alpha")
	_ = mkFile(t, db, sc, sub, "b.txt", "beta!")

	copied, err := db.CopyNode(ctx, sc, dir.ID, nil, nil, time.Now().UTC())
	if err != nil {
		t.Fatalf("copy: %v", err)
	}
	if copied.Name != "相册 - 副本" {
		t.Fatalf("copy name = %q", copied.Name)
	}
	if copied.Path != "相册 - 副本" || copied.ParentID == nil || *copied.ParentID != root.ID {
		t.Fatalf("copy path/parent = %q/%v", copied.Path, copied.ParentID)
	}

	// Source tree is untouched.
	srcChildren, _, err := db.ListChildren(ctx, sc, dir.ID, 50, 0)
	if err != nil || len(srcChildren) != 2 {
		t.Fatalf("source children = %d, err=%v", len(srcChildren), err)
	}
	// Copied tree has the same shape and paths.
	kids, _, err := db.ListChildren(ctx, sc, copied.ID, 50, 0)
	if err != nil || len(kids) != 2 {
		t.Fatalf("copied children = %+v, err=%v", kids, err)
	}
	var subCopy files.Node
	for _, k := range kids {
		if k.Name == "2024" {
			subCopy = k
		}
	}
	if subCopy.Path != "相册 - 副本/2024" {
		t.Fatalf("copied sub path = %q", subCopy.Path)
	}
	grand, _, err := db.ListChildren(ctx, sc, subCopy.ID, 50, 0)
	if err != nil || len(grand) != 1 || grand[0].Path != "相册 - 副本/2024/b.txt" {
		t.Fatalf("copied grand children = %+v, err=%v", grand, err)
	}

	var used int64
	if err := db.QueryRowContext(ctx, `SELECT used_bytes FROM users WHERE id = ?`, u.ID).Scan(&used); err != nil {
		t.Fatalf("used: %v", err)
	}
	if used != int64(len("alpha")+len("beta!"))*2 {
		t.Fatalf("used_bytes = %d", used)
	}
	var refs int
	if err := db.QueryRowContext(ctx, `SELECT SUM(refcount) FROM blobs`).Scan(&refs); err != nil {
		t.Fatalf("refs: %v", err)
	}
	if refs != 4 { // 2 files x 2 references
		t.Fatalf("refcount sum = %d", refs)
	}
}

// TestCopyNameConflictAndExplicitName covers the automatic free-name path.
func TestCopyNameConflictAndExplicitName(t *testing.T) {
	ctx := context.Background()
	db, policyID := newTestDB(t)
	u := seedUser(t, db, "alice", "user")
	sc := access.UserScope(u.ID, false)
	root, _ := db.EnsureRoot(ctx, u.ID, policyID)
	f := mkFile(t, db, sc, root, "notes.txt", "hello")
	first, err := db.CopyNode(ctx, sc, f.ID, nil, nil, time.Now().UTC())
	if err != nil {
		t.Fatalf("copy 1: %v", err)
	}
	if first.Name != "notes.txt - 副本" {
		t.Fatalf("first copy name = %q", first.Name)
	}
	second, err := db.CopyNode(ctx, sc, f.ID, nil, nil, time.Now().UTC())
	if err != nil {
		t.Fatalf("copy 2: %v", err)
	}
	if second.Name != "notes.txt - 副本 (1)" {
		t.Fatalf("second copy name = %q", second.Name)
	}
	explicit := "自定义.txt"
	third, err := db.CopyNode(ctx, sc, f.ID, &root.ID, &explicit, time.Now().UTC())
	if err != nil {
		t.Fatalf("copy explicit: %v", err)
	}
	if third.Name != "自定义.txt" {
		t.Fatalf("explicit name = %q", third.Name)
	}
}

// TestCopyRejectsInvalidTargetsAndQuota guards the unsafe paths.
func TestCopyRejectsInvalidTargetsAndQuota(t *testing.T) {
	ctx := context.Background()
	db, policyID := newTestDB(t)
	u := seedUser(t, db, "alice", "user")
	sc := access.UserScope(u.ID, false)
	root, _ := db.EnsureRoot(ctx, u.ID, policyID)
	dir, _ := db.CreateDir(ctx, sc, root.ID, "docs", files.ChildPath(root.Path, "docs"))
	sub, _ := db.CreateDir(ctx, sc, dir.ID, "inner", files.ChildPath(dir.Path, "inner"))
	file := mkFile(t, db, sc, dir, "x.txt", "0123456789")

	if _, err := db.CopyNode(ctx, sc, root.ID, nil, nil, time.Now().UTC()); apperr.CodeOf(err) != apperr.CodeInvalidTarget {
		t.Fatalf("copy root err = %v", err)
	}
	if _, err := db.CopyNode(ctx, sc, dir.ID, &sub.ID, nil, time.Now().UTC()); apperr.CodeOf(err) != apperr.CodeInvalidTarget {
		t.Fatalf("copy into descendant err = %v", err)
	}
	if _, err := db.CopyNode(ctx, sc, dir.ID, &file.ID, nil, time.Now().UTC()); apperr.CodeOf(err) != apperr.CodeParentNotFound {
		t.Fatalf("copy into file err = %v", err)
	}
	// Cross-owner scopes must not see the node.
	other := seedUser(t, db, "bob", "user")
	if _, err := db.CopyNode(ctx, access.UserScope(other.ID, false), file.ID, nil, nil, time.Now().UTC()); apperr.CodeOf(err) != apperr.CodeNotFound {
		t.Fatalf("cross owner err = %v", err)
	}
	// Quota: 10 bytes already used by x.txt, allow 12 total, copying 10 more fails.
	if _, err := db.ExecContext(ctx, `UPDATE users SET quota_bytes = 12 WHERE id = ?`, u.ID); err != nil {
		t.Fatalf("set quota: %v", err)
	}
	if _, err := db.CopyNode(ctx, sc, file.ID, nil, nil, time.Now().UTC()); apperr.CodeOf(err) != apperr.CodeQuotaExceeded {
		t.Fatalf("quota err = %v", err)
	}
}
