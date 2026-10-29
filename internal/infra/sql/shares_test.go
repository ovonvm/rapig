package sqlstore

import (
	"context"
	"testing"
	"time"

	"rapig/internal/domain/access"
	"rapig/internal/domain/share"
	"rapig/internal/platform/apperr"
)

func seedShareTree(t *testing.T, db *DB, policyID int64) (userID int64, sc access.Scope, dirID, fileID int64) {
	t.Helper()
	ctx := context.Background()
	u := seedUser(t, db, "alice", "user")
	sc = access.UserScope(u.ID, false)
	root, err := db.EnsureRoot(ctx, u.ID, policyID)
	if err != nil {
		t.Fatalf("root: %v", err)
	}
	dir, err := db.CreateDir(ctx, sc, root.ID, "docs", "docs")
	if err != nil {
		t.Fatalf("dir: %v", err)
	}
	blob, _, err := db.InsertBlob(ctx, "abc123", 5)
	if err != nil {
		t.Fatalf("blob: %v", err)
	}
	node, err := db.CommitCompletedUpload(ctx, sc, CompletedUploadInput{
		ParentID: dir.ID, Name: "a.txt", Path: "docs/a.txt", Mime: "text/plain",
		SHA256: "abc123", Size: 5, BlobID: blob.ID, CreatedAt: time.Now(),
	})
	if err != nil {
		t.Fatalf("file: %v", err)
	}
	return u.ID, sc, dir.ID, node.ID
}

func newShare(t *testing.T, db *DB, sc access.Scope, nodeID int64) share.Share {
	t.Helper()
	sh, err := db.CreateShare(context.Background(), sc, share.Share{
		ID: "id-" + time.Now().Format("150405.000000"), Token: "tok-" + time.Now().Format("150405.000000"),
		OwnerID: sc.UserID, NodeID: nodeID, Kind: share.KindFile, CreatedAt: time.Now(),
	}, []int64{nodeID})
	if err != nil {
		t.Fatalf("create share: %v", err)
	}
	return sh
}

func TestShareLifecycleAndScoping(t *testing.T) {
	db, policyID := newTestDB(t)
	ctx := context.Background()
	_, sc, dirID, _ := seedShareTree(t, db, policyID)

	sh := newShare(t, db, sc, dirID)
	got, err := db.ShareByToken(ctx, sh.Token)
	if err != nil || got.ID != sh.ID || got.Kind != share.KindFile {
		t.Fatalf("ShareByToken = %+v, %v", got, err)
	}

	list, total, err := db.ListShares(ctx, sc, "", 50, 0, time.Now())
	if err != nil || total != 1 || len(list) != 1 {
		t.Fatalf("list = %d %d %v", len(list), total, err)
	}

	other := seedUser(t, db, "bob", "user")
	if _, total, err := db.ListShares(ctx, access.UserScope(other.ID, false), "", 50, 0, time.Now()); err != nil || total != 0 {
		t.Fatalf("bob list = %d %v, want 0", total, err)
	}
	if _, err := db.ShareByID(ctx, access.UserScope(other.ID, false), sh.ID); apperr.CodeOf(err) != apperr.CodeNotFound {
		t.Fatalf("bob ShareByID = %v, want not_found", err)
	}

	ok, err := db.RevokeShare(ctx, sc, sh.ID, time.Now())
	if err != nil || !ok {
		t.Fatalf("revoke = %v %v", ok, err)
	}
	ok, err = db.RevokeShare(ctx, sc, sh.ID, time.Now())
	if err != nil || ok {
		t.Fatalf("second revoke = %v %v, want false nil", ok, err)
	}
	got, err = db.ShareByToken(ctx, sh.Token)
	if err != nil || got.Active(time.Now()) {
		t.Fatalf("revoked share active: %+v %v", got, err)
	}
}

func TestShareCreateRequiresOwnedActiveNode(t *testing.T) {
	db, policyID := newTestDB(t)
	ctx := context.Background()
	_, sc, _, fileID := seedShareTree(t, db, policyID)
	other := seedUser(t, db, "bob", "user")
	osc := access.UserScope(other.ID, false)

	if _, err := db.CreateShare(ctx, osc, share.Share{
		ID: "x", Token: "y", OwnerID: other.ID, NodeID: fileID, Kind: share.KindFile, CreatedAt: time.Now(),
	}, []int64{fileID}); apperr.CodeOf(err) != apperr.CodeNotFound {
		t.Fatalf("foreign node create = %v, want not_found", err)
	}

	if _, err := db.TrashNode(ctx, sc, fileID, time.Now()); err != nil {
		t.Fatalf("trash: %v", err)
	}
	if _, err := db.CreateShare(ctx, sc, share.Share{
		ID: "z", Token: "w", OwnerID: sc.UserID, NodeID: fileID, Kind: share.KindFile, CreatedAt: time.Now(),
	}, []int64{fileID}); apperr.CodeOf(err) != apperr.CodeNotFound {
		t.Fatalf("trashed node create = %v, want not_found", err)
	}
}

func TestShareConsumeBudget(t *testing.T) {
	db, policyID := newTestDB(t)
	ctx := context.Background()
	_, sc, _, fileID := seedShareTree(t, db, policyID)

	sh, err := db.CreateShare(ctx, sc, share.Share{
		ID: "budget", Token: "tok-budget", OwnerID: sc.UserID, NodeID: fileID,
		Kind: share.KindFile, MaxDownloads: 1, CreatedAt: time.Now(),
	}, []int64{fileID})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	ok, err := db.TryConsumeDownload(ctx, sh.ID, time.Now())
	if err != nil || !ok {
		t.Fatalf("first consume = %v %v", ok, err)
	}
	ok, err = db.TryConsumeDownload(ctx, sh.ID, time.Now())
	if err != nil || ok {
		t.Fatalf("second consume = %v %v, want false", ok, err)
	}
	got, err := db.ShareByToken(ctx, sh.Token)
	if err != nil || got.DownloadCount != 1 {
		t.Fatalf("download count = %+v %v", got, err)
	}
}

func TestShareExpiryAndCleanup(t *testing.T) {
	db, policyID := newTestDB(t)
	ctx := context.Background()
	_, sc, _, fileID := seedShareTree(t, db, policyID)

	expired := time.Now().Add(-time.Minute)
	sh, err := db.CreateShare(ctx, sc, share.Share{
		ID: "exp", Token: "tok-exp", OwnerID: sc.UserID, NodeID: fileID,
		Kind: share.KindFile, ExpiresAt: &expired, CreatedAt: time.Now(),
	}, []int64{fileID})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	got, err := db.ShareByToken(ctx, sh.Token)
	if err != nil || got.Active(time.Now()) {
		t.Fatalf("expired share active: %+v %v", got, err)
	}
	if err := db.TouchVisit(ctx, sh.ID, time.Now()); err != nil {
		t.Fatalf("visit: %v", err)
	}
	if _, err := db.RevokeShare(ctx, sc, sh.ID, time.Now()); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	// Deleting dead rows uses created/revoked time vs a cutoff.
	n, err := db.DeleteDeadShares(ctx, time.Now().Add(48*time.Hour))
	if err != nil || n == 0 {
		t.Fatalf("cleanup = %d %v", n, err)
	}
	if _, err := db.ShareByToken(ctx, sh.Token); apperr.CodeOf(err) != apperr.CodeNotFound {
		t.Fatalf("share after cleanup = %v, want not_found", err)
	}
}

func TestShareMergedItemsLifecycle(t *testing.T) {
	db, policyID := newTestDB(t)
	ctx := context.Background()
	userID, sc, dirID, fileID := seedShareTree(t, db, policyID)
	now := time.Now()
	sh, err := db.CreateShare(ctx, sc, share.Share{
		ID: "multi1", Token: "tok-multi", OwnerID: userID, NodeID: fileID,
		Kind: share.KindMulti, CreatedAt: now,
	}, []int64{fileID, dirID})
	if err != nil {
		t.Fatalf("create multi: %v", err)
	}
	if sh.ItemCount() != 2 {
		t.Fatalf("items = %d, want 2", sh.ItemCount())
	}
	byTok, err := db.ShareByToken(ctx, "tok-multi")
	if err != nil || len(byTok.Items) != 2 || !byTok.Multi() {
		t.Fatalf("by token = %+v %v", byTok, err)
	}
	if _, total, err := db.ListShares(ctx, sc, "active", 50, 0, now); err != nil || total != 1 {
		t.Fatalf("active list = %d %v", total, err)
	}
	if _, err := db.RevokeShare(ctx, sc, sh.ID, now); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if _, total, err := db.ListShares(ctx, sc, "revoked", 50, 0, now); err != nil || total != 1 {
		t.Fatalf("revoked list = %d %v", total, err)
	}
	if ok, err := db.RestoreShare(ctx, sc, sh.ID, now); err != nil || !ok {
		t.Fatalf("restore = %v %v", ok, err)
	}
	if ok, err := db.PurgeShare(ctx, sc, sh.ID, now); err != nil || ok {
		t.Fatalf("purge active = %v %v, want false", ok, err)
	}
	if _, err := db.RevokeShare(ctx, sc, sh.ID, now); err != nil {
		t.Fatalf("re-revoke: %v", err)
	}
	if ok, err := db.PurgeShare(ctx, sc, sh.ID, now); err != nil || !ok {
		t.Fatalf("purge revoked = %v %v", ok, err)
	}
	if _, err := db.ShareByID(ctx, sc, sh.ID); apperr.CodeOf(err) != apperr.CodeNotFound {
		t.Fatalf("share after purge = %v, want not_found", err)
	}
	var n int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM share_items WHERE share_id = ?`, "multi1").Scan(&n); err != nil || n != 0 {
		t.Fatalf("share_items after purge = %d %v", n, err)
	}
}

func TestShareCreateRejectsRootAndForeignItem(t *testing.T) {
	db, policyID := newTestDB(t)
	ctx := context.Background()
	userID, sc, _, fileID := seedShareTree(t, db, policyID)
	root, err := db.RootNode(ctx, sc)
	if err != nil {
		t.Fatalf("root: %v", err)
	}
	if _, err := db.CreateShare(ctx, sc, share.Share{
		ID: "r", Token: "rt", OwnerID: userID, NodeID: root.ID, Kind: share.KindDir, CreatedAt: time.Now(),
	}, []int64{root.ID}); apperr.CodeOf(err) != apperr.CodeNotFound {
		t.Fatalf("root share = %v, want not_found", err)
	}
	other := seedUser(t, db, "bob", "user")
	osc := access.UserScope(other.ID, false)
	bobRoot, err := db.EnsureRoot(ctx, other.ID, policyID)
	if err != nil {
		t.Fatalf("bob root: %v", err)
	}
	bobDir, err := db.CreateDir(ctx, osc, bobRoot.ID, "bx", "bx")
	if err != nil {
		t.Fatalf("bob dir: %v", err)
	}
	if _, err := db.CreateShare(ctx, sc, share.Share{
		ID: "mixed", Token: "mixed-tok", OwnerID: userID, NodeID: fileID,
		Kind: share.KindMulti, CreatedAt: time.Now(),
	}, []int64{fileID, bobDir.ID}); apperr.CodeOf(err) != apperr.CodeNotFound {
		t.Fatalf("foreign item = %v, want not_found", err)
	}
	if _, err := db.ShareByToken(ctx, "mixed-tok"); apperr.CodeOf(err) != apperr.CodeNotFound {
		t.Fatalf("rolled-back share = %v, want not_found", err)
	}
}
