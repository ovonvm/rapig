package sqlstore

import (
	"context"
	"strings"
	"testing"
	"time"
)

// insertNodeForTest inserts one file node row directly. The store's higher-level
// helpers need a full upload/scope flow; purge accounting only depends on the
// columns written here, so raw rows keep the test focused.
func insertNodeForTest(t *testing.T, db *DB, ownerID, parentID, policyID int64, name string, blobID, size int64, state string) int64 {
	t.Helper()
	now := time.Now().Unix()
	res, err := db.ExecContext(context.Background(), `
		INSERT INTO nodes (owner_id, parent_id, kind, name, name_folded, path, blob_id, size, storage_policy_id, state, created_at, updated_at)
		VALUES (?, ?, 'file', ?, ?, '', ?, ?, ?, ?, ?, ?)`,
		ownerID, parentID, name, strings.ToLower(name), blobID, size, policyID, state, now, now)
	if err != nil {
		t.Fatalf("insert node %q: %v", name, err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		t.Fatalf("node id: %v", err)
	}
	return id
}

// insertRootDirForTest inserts the owner's root directory. 0013 requires every
// node to carry a live storage policy, including directories.
func insertRootDirForTest(t *testing.T, db *DB, ownerID, policyID int64) int64 {
	t.Helper()
	now := time.Now().Unix()
	res, err := db.ExecContext(context.Background(), `
		INSERT INTO nodes (owner_id, parent_id, kind, name, name_folded, path, blob_id, size, storage_policy_id, state, created_at, updated_at)
		VALUES (?, NULL, 'dir', 'root', 'root', '', NULL, 0, ?, 'active', ?, ?)`,
		ownerID, policyID, now, now)
	if err != nil {
		t.Fatalf("insert root dir: %v", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		t.Fatalf("root node id: %v", err)
	}
	return id
}

func setBlobRefcountForTest(t *testing.T, db *DB, blobID int64, refcount int) {
	t.Helper()
	if _, err := db.ExecContext(context.Background(), `UPDATE blobs SET refcount = ? WHERE id = ?`, refcount, blobID); err != nil {
		t.Fatalf("set refcount: %v", err)
	}
}

func countNodesForTest(t *testing.T, db *DB, ownerID int64) int {
	t.Helper()
	var n int
	if err := db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM nodes WHERE owner_id = ?`, ownerID).Scan(&n); err != nil {
		t.Fatalf("count nodes: %v", err)
	}
	return n
}

// TestPurgeUserAccountReleasesBlobRefcount covers F-46: deleting an account
// must release exactly the references its own nodes held (active and trashed),
// leaving blobs shared with other users intact.
func TestPurgeUserAccountReleasesBlobRefcount(t *testing.T) {
	ctx := context.Background()
	db, policyID := newTestDB(t)

	alice := seedUser(t, db, "alice", "user")
	bob := seedUser(t, db, "bob", "user")
	aliceRoot := insertRootDirForTest(t, db, alice.ID, policyID)
	bobRoot := insertRootDirForTest(t, db, bob.ID, policyID)

	blobShared, _, err := db.InsertBlobInPolicy(ctx, policyID, strings.Repeat("a", 64), 10)
	if err != nil {
		t.Fatalf("blob shared: %v", err)
	}
	blobAliceOnly, _, err := db.InsertBlobInPolicy(ctx, policyID, strings.Repeat("b", 64), 20)
	if err != nil {
		t.Fatalf("blob alice-only: %v", err)
	}

	insertNodeForTest(t, db, alice.ID, aliceRoot, policyID, "a.txt", blobShared.ID, 10, "active")
	// A trashed node still holds a reference until the account is purged.
	insertNodeForTest(t, db, alice.ID, aliceRoot, policyID, "b.txt", blobAliceOnly.ID, 20, "trashed")
	insertNodeForTest(t, db, bob.ID, bobRoot, policyID, "a.txt", blobShared.ID, 10, "active")
	setBlobRefcountForTest(t, db, blobShared.ID, 2)
	setBlobRefcountForTest(t, db, blobAliceOnly.ID, 1)

	now := time.Now()
	if err := db.PurgeUserAccount(ctx, alice, now, now.Add(time.Hour)); err != nil {
		t.Fatalf("purge alice: %v", err)
	}

	if n := countNodesForTest(t, db, alice.ID); n != 0 {
		t.Errorf("alice still has %d nodes", n)
	}
	if n := countNodesForTest(t, db, bob.ID); n != 2 {
		t.Errorf("bob nodes = %d, want 2", n)
	}
	shared, err := db.BlobByID(ctx, blobShared.ID)
	if err != nil {
		t.Fatalf("load shared blob: %v", err)
	}
	if shared.Refcount != 1 {
		t.Errorf("shared refcount = %d, want 1", shared.Refcount)
	}
	only, err := db.BlobByID(ctx, blobAliceOnly.ID)
	if err != nil {
		t.Fatalf("load alice-only blob: %v", err)
	}
	if only.Refcount != 0 {
		t.Errorf("alice-only refcount = %d, want 0", only.Refcount)
	}

	// Purging the last owner must leave every blob at zero so the next gc run
	// can reclaim the files without a fsck repair.
	if err := db.PurgeUserAccount(ctx, bob, now, now.Add(time.Hour)); err != nil {
		t.Fatalf("purge bob: %v", err)
	}
	shared, err = db.BlobByID(ctx, blobShared.ID)
	if err != nil {
		t.Fatalf("reload shared blob: %v", err)
	}
	if shared.Refcount != 0 {
		t.Errorf("shared refcount after both purges = %d, want 0", shared.Refcount)
	}
}

// TestPurgeUserAccountWithoutBlobs ensures the release statement does not break
// accounts that only own directories.
func TestPurgeUserAccountWithoutBlobs(t *testing.T) {
	ctx := context.Background()
	db, policyID := newTestDB(t)
	carol := seedUser(t, db, "carol", "user")
	insertRootDirForTest(t, db, carol.ID, policyID)

	now := time.Now()
	if err := db.PurgeUserAccount(ctx, carol, now, now.Add(time.Hour)); err != nil {
		t.Fatalf("purge carol: %v", err)
	}
	if n := countNodesForTest(t, db, carol.ID); n != 0 {
		t.Errorf("carol still has %d nodes", n)
	}
}
