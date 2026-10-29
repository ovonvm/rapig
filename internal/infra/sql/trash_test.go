package sqlstore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"testing"
	"time"

	"rapig/internal/domain/access"
	"rapig/internal/domain/files"
	"rapig/internal/platform/apperr"
)

// mkFile inserts a file node whose blob hashes content under parent.
func mkFile(t *testing.T, db *DB, sc access.Scope, parent files.Node, name, content string) files.Node {
	t.Helper()
	ctx := context.Background()
	sum := sha256.Sum256([]byte(content))
	blob, _, err := db.InsertBlob(ctx, hex.EncodeToString(sum[:]), int64(len(content)))
	if err != nil {
		t.Fatalf("insert blob: %v", err)
	}
	n, err := db.CommitCompletedUpload(ctx, sc, CompletedUploadInput{
		ParentID: parent.ID,
		Name:     name,
		Path:     files.ChildPath(parent.Path, name),
		Mime:     "text/plain",
		SHA256:   blob.SHA256,
		Size:     int64(len(content)),
		BlobID:   blob.ID,
	})
	if err != nil {
		t.Fatalf("commit file %q: %v", name, err)
	}
	return n
}

func TestTrashLifecycle(t *testing.T) {
	db, policyID := newTestDB(t)
	ctx := context.Background()
	u := seedUser(t, db, "alice", "user")
	sc := access.UserScope(u.ID, false)
	root, err := db.EnsureRoot(ctx, u.ID, policyID)
	if err != nil {
		t.Fatalf("root: %v", err)
	}
	a, err := db.CreateDir(ctx, sc, root.ID, "A", "A")
	if err != nil {
		t.Fatalf("create A: %v", err)
	}
	child, err := db.CreateDir(ctx, sc, a.ID, "inner", "A/inner")
	if err != nil {
		t.Fatalf("create child: %v", err)
	}

	trashed, err := db.TrashNode(ctx, sc, a.ID, time.Now())
	if err != nil {
		t.Fatalf("trash: %v", err)
	}
	if trashed.State != files.StateTrashed || trashed.TrashedFrom == nil || *trashed.TrashedFrom != root.ID {
		t.Fatalf("trashed node = %+v", trashed)
	}
	// Gone from normal listing.
	if _, total, err := db.ListChildren(ctx, sc, root.ID, 50, 0); err != nil || total != 0 {
		t.Fatalf("children after trash: total=%d err=%v", total, err)
	}
	// Trash lists only the top-level entry, not its still-active child.
	entries, total, err := db.ListTrash(ctx, sc, 50, 0)
	if err != nil {
		t.Fatalf("list trash: %v", err)
	}
	if total != 1 || len(entries) != 1 || entries[0].ID != a.ID {
		t.Fatalf("trash entries = %+v total=%d", entries, total)
	}

	restored, err := db.RestoreNode(ctx, sc, a.ID, time.Now())
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	if restored.State != files.StateActive || restored.ParentID == nil || *restored.ParentID != root.ID || restored.Path != "A" {
		t.Fatalf("restored = %+v", restored)
	}
	got, err := db.NodeByID(ctx, sc, child.ID)
	if err != nil || got.Path != "A/inner" {
		t.Fatalf("child after restore: %+v err=%v", got, err)
	}
}

func TestTrashRestoreRenamesOnConflictAndRootFallback(t *testing.T) {
	db, policyID := newTestDB(t)
	ctx := context.Background()
	u := seedUser(t, db, "alice", "user")
	sc := access.UserScope(u.ID, false)
	root, err := db.EnsureRoot(ctx, u.ID, policyID)
	if err != nil {
		t.Fatalf("root: %v", err)
	}
	parent, err := db.CreateDir(ctx, sc, root.ID, "docs", "docs")
	if err != nil {
		t.Fatalf("create docs: %v", err)
	}
	victim, err := db.CreateDir(ctx, sc, parent.ID, "x", "docs/x")
	if err != nil {
		t.Fatalf("create x: %v", err)
	}
	if _, err := db.TrashNode(ctx, sc, victim.ID, time.Now()); err != nil {
		t.Fatalf("trash x: %v", err)
	}
	// New "x" takes the original name, restore must pick "x (1)".
	if _, err := db.CreateDir(ctx, sc, parent.ID, "x", "docs/x"); err != nil {
		t.Fatalf("create new x: %v", err)
	}
	restored, err := db.RestoreNode(ctx, sc, victim.ID, time.Now())
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	if restored.Name != "x (1)" || restored.Path != "docs/x (1)" {
		t.Fatalf("restored name/path = %q %q", restored.Name, restored.Path)
	}
	// Parent deleted meanwhile: fall back to root. Simulate a purged parent
	// (its subtree is not touched) so the original parent can no longer be a
	// restore target.
	if _, err := db.TrashNode(ctx, sc, restored.ID, time.Now()); err != nil {
		t.Fatalf("trash restored: %v", err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE nodes SET state = 'purged' WHERE id = ?`, parent.ID); err != nil {
		t.Fatalf("purge parent row: %v", err)
	}
	back, err := db.RestoreNode(ctx, sc, restored.ID, time.Now())
	if err != nil {
		t.Fatalf("restore without parent: %v", err)
	}
	if back.ParentID == nil || *back.ParentID != root.ID {
		t.Fatalf("restore fallback parent = %+v", back.ParentID)
	}
}

func TestPurgeReleasesQuotaAndRefcount(t *testing.T) {
	db, policyID := newTestDB(t)
	ctx := context.Background()
	u := seedUser(t, db, "alice", "user")
	sc := access.UserScope(u.ID, false)
	root, err := db.EnsureRoot(ctx, u.ID, policyID)
	if err != nil {
		t.Fatalf("root: %v", err)
	}
	file := mkFile(t, db, sc, root, "big.bin", "0123456789")
	if _, err := db.ExecContext(ctx, `UPDATE users SET used_bytes = 10 WHERE id = ?`, u.ID); err != nil {
		t.Fatalf("set used: %v", err)
	}
	if _, err := db.TrashNode(ctx, sc, file.ID, time.Now()); err != nil {
		t.Fatalf("trash file: %v", err)
	}
	orphans, err := db.PurgeNode(ctx, sc, file.ID, time.Now())
	if err != nil {
		t.Fatalf("purge: %v", err)
	}
	if len(orphans) != 1 {
		t.Fatalf("orphans = %v", orphans)
	}
	if _, err := db.NodeByID(ctx, sc, file.ID); apperr.CodeOf(err) != apperr.CodeNotFound {
		t.Fatalf("node after purge = %v", err)
	}
	var used int64
	if err := db.QueryRowContext(ctx, `SELECT used_bytes FROM users WHERE id = ?`, u.ID).Scan(&used); err != nil {
		t.Fatalf("used: %v", err)
	}
	if used != 0 {
		t.Fatalf("used after purge = %d", used)
	}
	var blobs int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM blobs`).Scan(&blobs); err != nil {
		t.Fatalf("blobs: %v", err)
	}
	if blobs != 0 {
		t.Fatalf("blob rows after purge = %d", blobs)
	}
}

func TestMoveNodeAndGuards(t *testing.T) {
	db, policyID := newTestDB(t)
	ctx := context.Background()
	u := seedUser(t, db, "alice", "user")
	sc := access.UserScope(u.ID, false)
	root, err := db.EnsureRoot(ctx, u.ID, policyID)
	if err != nil {
		t.Fatalf("root: %v", err)
	}
	a, err := db.CreateDir(ctx, sc, root.ID, "A", "A")
	if err != nil {
		t.Fatalf("A: %v", err)
	}
	b, err := db.CreateDir(ctx, sc, root.ID, "B", "B")
	if err != nil {
		t.Fatalf("B: %v", err)
	}
	inner, err := db.CreateDir(ctx, sc, a.ID, "inner", "A/inner")
	if err != nil {
		t.Fatalf("inner: %v", err)
	}

	// Rename, then move into B, child path follows.
	bName := "B2"
	moved, err := db.MoveNode(ctx, sc, b.ID, nil, &bName, b.Revision(), time.Now())
	if err != nil {
		t.Fatalf("rename: %v", err)
	}
	if moved.Name != "B2" {
		t.Fatalf("renamed = %q", moved.Name)
	}
	rootRef := int64(0)
	_ = rootRef
	moved, err = db.MoveNode(ctx, sc, b.ID, &a.ID, nil, moved.Revision(), time.Now())
	if err != nil {
		t.Fatalf("move: %v", err)
	}
	if moved.Path != "A/B2" {
		t.Fatalf("moved path = %q", moved.Path)
	}

	if _, err := db.MoveNode(ctx, sc, a.ID, &inner.ID, nil, a.Revision(), time.Now()); apperr.CodeOf(err) != apperr.CodeInvalidTarget {
		t.Fatalf("move into descendant = %v", err)
	}
	if _, err := db.MoveNode(ctx, sc, inner.ID, nil, nil, "stale-token", time.Now()); apperr.CodeOf(err) != apperr.CodeStaleWrite {
		t.Fatalf("stale revision = %v", err)
	}
	if _, err := db.MoveNode(ctx, sc, inner.ID, nil, nil, "2026-01-01T00:00:00Z", time.Now()); apperr.CodeOf(err) != apperr.CodeStaleWrite {
		t.Fatalf("old revision = %v", err)
	}
}

func TestEmptyTrashCountsTopLevel(t *testing.T) {
	db, policyID := newTestDB(t)
	ctx := context.Background()
	u := seedUser(t, db, "alice", "user")
	sc := access.UserScope(u.ID, false)
	root, err := db.EnsureRoot(ctx, u.ID, policyID)
	if err != nil {
		t.Fatalf("root: %v", err)
	}
	for _, name := range []string{"a", "b"} {
		n, err := db.CreateDir(ctx, sc, root.ID, name, name)
		if err != nil {
			t.Fatalf("create %s: %v", name, err)
		}
		if _, err := db.TrashNode(ctx, sc, n.ID, time.Now()); err != nil {
			t.Fatalf("trash %s: %v", name, err)
		}
	}
	// Both entries are top-level, so both are counted.
	entries, _, err := db.ListTrash(ctx, sc, 50, 0)
	if err != nil || len(entries) != 2 {
		t.Fatalf("entries = %+v err=%v", entries, err)
	}
	n, orphans, err := db.EmptyTrash(ctx, sc, time.Now())
	if err != nil {
		t.Fatalf("empty: %v", err)
	}
	if n != 2 || len(orphans) != 0 {
		t.Fatalf("empty = %d orphans=%v", n, orphans)
	}
	if _, total, err := db.ListTrash(ctx, sc, 50, 0); err != nil || total != 0 {
		t.Fatalf("trash after empty: total=%d err=%v", total, err)
	}
}

// TestPurgeNodeReferencedByCompletedUpload reproduces the Linux acceptance
// failure: a completed upload row pointed at the node (uploads.node_id had
// NO ACTION), so deleting the node hit a foreign key error. Migration 0007
// rebuilds uploads with ON DELETE SET NULL.
func TestPurgeNodeReferencedByCompletedUpload(t *testing.T) {
	db, policyID := newTestDB(t)
	ctx := context.Background()
	u := seedUser(t, db, "bob", "user")
	sc := access.UserScope(u.ID, false)
	root, err := db.EnsureRoot(ctx, u.ID, policyID)
	if err != nil {
		t.Fatalf("root: %v", err)
	}
	now := time.Now()
	content := "hello"
	sum := sha256.Sum256([]byte(content))
	sha := hex.EncodeToString(sum[:])
	blob, _, err := db.InsertBlob(ctx, sha, int64(len(content)))
	if err != nil {
		t.Fatalf("blob: %v", err)
	}
	if err := db.ReserveBytes(ctx, u.ID, int64(len(content))); err != nil {
		t.Fatalf("reserve: %v", err)
	}
	if err := db.CreateUpload(ctx, sc, files.Upload{
		ID:            "up-purge-1",
		OwnerID:       u.ID,
		ParentID:      root.ID,
		Name:          "f.txt",
		NameFolded:    "f.txt",
		Size:          int64(len(content)),
		SHA256:        sha,
		PartSize:      files.PartSizeDefault,
		PartsTotal:    1,
		ReceivedBytes: int64(len(content)),
		State:         files.UploadOpen,
		CreatedAt:     now,
		ExpiresAt:     now.Add(files.UploadTTL),
	}); err != nil {
		t.Fatalf("create upload: %v", err)
	}
	node, err := db.CommitCompletedUpload(ctx, sc, CompletedUploadInput{
		UploadID:     "up-purge-1",
		ParentID:     root.ID,
		Name:         "f.txt",
		Path:         files.ChildPath(root.Path, "f.txt"),
		Mime:         "text/plain",
		SHA256:       sha,
		Size:         int64(len(content)),
		BlobID:       blob.ID,
		IncrementRef: false,
		CreatedAt:    now,
	})
	if err != nil {
		t.Fatalf("commit: %v", err)
	}
	if _, err := db.TrashNode(ctx, sc, node.ID, now); err != nil {
		t.Fatalf("trash: %v", err)
	}
	orphans, err := db.PurgeNode(ctx, sc, node.ID, now)
	if err != nil {
		t.Fatalf("purge node referenced by completed upload: %v", err)
	}
	if len(orphans) != 1 {
		t.Fatalf("orphans = %v, want one blob", orphans)
	}
}
