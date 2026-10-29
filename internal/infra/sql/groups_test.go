package sqlstore

import (
	"context"
	"strings"
	"testing"
	"time"

	"rapig/internal/domain/access"
	domaingroups "rapig/internal/domain/groups"
	"rapig/internal/platform/apperr"
)

// nodeRef is a tiny node locator (id + subtree path) used to build parents for
// the raw inserts below without another round trip.
type nodeRef struct {
	ID   int64
	Path string
}

// createTestGroup inserts a group plus its root node, with managerID as the
// first manager, and returns the group and a reference to its root node.
func createTestGroup(t *testing.T, db *DB, name string, createdBy, managerID int64) (domaingroups.Group, nodeRef) {
	t.Helper()
	creator := createdBy
	g, rootID, err := db.CreateGroup(context.Background(), domaingroups.Group{
		Name:        name,
		NameFolded:  domaingroups.FoldName(name),
		CreatedBy:   &creator,
		MemberPerms: domaingroups.DefaultMemberPerms(),
		CreatedAt:   time.Now().UTC(),
	}, managerID)
	if err != nil {
		t.Fatalf("create group %q: %v", name, err)
	}
	root, err := db.GroupRootNode(context.Background(), access.UserGroupScope(createdBy, false, []int64{g.ID}), g.ID)
	if err != nil {
		t.Fatalf("group root %q: %v", name, err)
	}
	if root.ID != rootID {
		t.Fatalf("group root id = %d, want %d", root.ID, rootID)
	}
	return g, nodeRef{ID: root.ID, Path: root.Path}
}

// insertFileNode writes a file node directly; the upload path needs parts and
// blobs and would obscure what these tests actually check.
func insertFileNode(t *testing.T, db *DB, ownerID int64, parent nodeRef, groupID *int64, name, sha string, size int64) int64 {
	t.Helper()
	now := time.Now().UTC().Unix()
	path := name
	if parent.Path != "" {
		path = parent.Path + "/" + name
	}
	res, err := db.ExecContext(context.Background(), `
		INSERT INTO nodes (owner_id, parent_id, kind, name, name_folded, path, blob_id, size,
			storage_policy_id, state, group_id, created_at, updated_at)
		VALUES (?, ?, 'file', ?, ?, ?, (SELECT id FROM blobs WHERE sha256 = ?), ?,
			(SELECT id FROM storage_policies WHERE is_default = 1), 'active', ?, ?, ?)`,
		ownerID, parent.ID, name, strings.ToLower(name), path, sha, size, groupID, now, now)
	if err != nil {
		t.Fatalf("insert file node %q: %v", name, err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		t.Fatalf("file node id: %v", err)
	}
	return id
}

func userUsedBytes(t *testing.T, db *DB, userID int64) int64 {
	t.Helper()
	var used int64
	if err := db.QueryRowContext(context.Background(),
		`SELECT used_bytes FROM users WHERE id = ?`, userID).Scan(&used); err != nil {
		t.Fatalf("used_bytes of %d: %v", userID, err)
	}
	return used
}

func blobRefcount(t *testing.T, db *DB, sha string) int {
	t.Helper()
	var refcount int
	if err := db.QueryRowContext(context.Background(),
		`SELECT refcount FROM blobs WHERE sha256 = ?`, sha).Scan(&refcount); err != nil {
		t.Fatalf("blob refcount %q: %v", sha, err)
	}
	return refcount
}

// TestGroupScopeIsolation proves the read predicate keeps the two spaces
// apart: a personal scope never sees group nodes, and a member's group scope
// never sees another user's personal nodes.
func TestGroupScopeIsolation(t *testing.T) {
	db, policyID := newTestDB(t)
	ctx := context.Background()
	alice := seedUser(t, db, "alice", "user")
	bob := seedUser(t, db, "bob", "user")

	aliceRoot, err := db.EnsureRoot(ctx, alice.ID, policyID)
	if err != nil {
		t.Fatalf("alice root: %v", err)
	}
	aliceDir, err := db.CreateDir(ctx, access.UserScope(alice.ID, false), aliceRoot.ID, "personal", "personal")
	if err != nil {
		t.Fatalf("alice dir: %v", err)
	}

	g, root := createTestGroup(t, db, "团队", alice.ID, alice.ID)
	if err := db.AddGroupMember(ctx, g.ID, bob.ID, domaingroups.RoleMember, time.Now().UTC()); err != nil {
		t.Fatalf("add bob: %v", err)
	}
	aliceGroupScope := access.UserGroupScope(alice.ID, false, []int64{g.ID})
	groupDir, err := db.CreateDir(ctx, aliceGroupScope, root.ID, "共享", "共享")
	if err != nil {
		t.Fatalf("group dir: %v", err)
	}
	if groupDir.GroupID == nil || *groupDir.GroupID != g.ID {
		t.Fatalf("group dir GroupID = %v, want %d", groupDir.GroupID, g.ID)
	}

	alicePersonal := access.UserScope(alice.ID, false)
	if _, err := db.NodeByID(ctx, alicePersonal, groupDir.ID); apperr.CodeOf(err) != apperr.CodeNotFound {
		t.Errorf("personal scope saw group node: %v", err)
	}
	items, total, err := db.ListChildren(ctx, alicePersonal, root.ID, 50, 0)
	if err != nil || total != 0 || len(items) != 0 {
		t.Errorf("personal ListChildren(group root) = %d/%d, %v", len(items), total, err)
	}
	rootNode, err := db.RootNode(ctx, alicePersonal)
	if err != nil || rootNode.ID != aliceRoot.ID {
		t.Errorf("personal RootNode = %+v, %v", rootNode, err)
	}
	personalHits, _, err := db.SearchNodes(ctx, alicePersonal, "共享", "", false, 50, 0)
	if err != nil || len(personalHits) != 0 {
		t.Errorf("personal search leaked %d group nodes, %v", len(personalHits), err)
	}

	bobGroupScope := access.UserGroupScope(bob.ID, false, []int64{g.ID})
	if got, err := db.NodeByID(ctx, bobGroupScope, groupDir.ID); err != nil || got.ID != groupDir.ID {
		t.Errorf("member cannot see group node: %+v, %v", got, err)
	}
	if _, err := db.NodeByID(ctx, bobGroupScope, aliceDir.ID); apperr.CodeOf(err) != apperr.CodeNotFound {
		t.Errorf("group scope saw another user's personal node: %v", err)
	}
	groupHits, _, err := db.SearchGroupNodes(ctx, bobGroupScope, g.ID, "共享", "", false, 50, 0)
	if err != nil || len(groupHits) != 1 || groupHits[0].ID != groupDir.ID {
		t.Errorf("group search = %+v, %v", groupHits, err)
	}
}

// TestGroupRootIsUnique checks the partial unique index on (group_id) for root
// rows and that a group root is never returned as someone's personal root.
func TestGroupRootIsUnique(t *testing.T) {
	db, policyID := newTestDB(t)
	ctx := context.Background()
	alice := seedUser(t, db, "alice", "user")
	root, err := db.EnsureRoot(ctx, alice.ID, policyID)
	if err != nil {
		t.Fatalf("root: %v", err)
	}
	g, groupRoot := createTestGroup(t, db, "唯一根", alice.ID, alice.ID)

	now := time.Now().UTC().Unix()
	_, err = db.ExecContext(ctx, `
		INSERT INTO nodes (owner_id, parent_id, kind, name, name_folded, path, state, group_id, created_at, updated_at)
		VALUES (?, NULL, 'dir', '', '', '', 'active', ?, ?, ?)`,
		alice.ID, g.ID, now, now)
	if err == nil {
		t.Fatal("second root for the same group was accepted")
	}
	if !isUniqueViolation(err) {
		t.Errorf("second group root error = %v, want unique violation", err)
	}
	if got, err := db.RootNode(ctx, access.UserScope(alice.ID, false)); err != nil || got.ID != root.ID {
		t.Errorf("RootNode = %+v, %v; want personal root %d", got, err, root.ID)
	}
	if groupRoot.ID == root.ID {
		t.Errorf("group root shares the personal root id %d", root.ID)
	}
}

// TestGroupMembershipCRUD covers the member table plus the emptiness check the
// delete flow relies on.
func TestGroupMembershipCRUD(t *testing.T) {
	db, _ := newTestDB(t)
	ctx := context.Background()
	alice := seedUser(t, db, "alice", "user")
	bob := seedUser(t, db, "bob", "user")
	carol := seedUser(t, db, "carol", "user")

	g, _ := createTestGroup(t, db, "成员", alice.ID, alice.ID)
	busy, err := db.GroupHasContent(ctx, g.ID)
	if err != nil || busy {
		t.Fatalf("fresh group has content = %v, %v", busy, err)
	}

	if err := db.AddGroupMember(ctx, g.ID, bob.ID, domaingroups.RoleMember, time.Now().UTC()); err != nil {
		t.Fatalf("add bob: %v", err)
	}
	if err := db.AddGroupMember(ctx, g.ID, carol.ID, domaingroups.RoleManager, time.Now().UTC()); err != nil {
		t.Fatalf("add carol: %v", err)
	}
	if err := db.AddGroupMember(ctx, g.ID, bob.ID, domaingroups.RoleMember, time.Now().UTC()); apperr.CodeOf(err) != apperr.CodeInvalidState {
		t.Errorf("duplicate member = %v, want invalid_state", err)
	}

	members, err := db.ListGroupMembers(ctx, g.ID)
	if err != nil || len(members) != 3 {
		t.Fatalf("members = %d, %v", len(members), err)
	}
	if members[0].UserID != alice.ID || members[1].UserID != carol.ID || members[2].UserID != bob.ID {
		t.Errorf("member order = %+v", members)
	}
	if n, err := db.CountGroupManagers(ctx, g.ID); err != nil || n != 2 {
		t.Errorf("managers = %d, %v; want 2", n, err)
	}

	roles, err := db.GroupRolesForUser(ctx, bob.ID)
	if err != nil {
		t.Fatalf("roles for bob: %v", err)
	}
	info, ok := roles[g.ID]
	if !ok || info.Role != access.GroupRoleMember {
		t.Fatalf("bob group role = %+v ok=%v", info, ok)
	}
	if info.Perms.Has(access.ActionWrite) || !info.Perms.Has(access.ActionRead) {
		t.Errorf("member perms = %v, want read only", info.Perms.List())
	}
	managerRoles, err := db.GroupRolesForUser(ctx, alice.ID)
	if err != nil {
		t.Fatalf("roles for alice: %v", err)
	}
	for _, a := range domaingroups.FileActions {
		if !managerRoles[g.ID].Perms.Has(a) {
			t.Errorf("manager lacks %s", a)
		}
	}

	if err := db.UpdateGroupMemberRole(ctx, g.ID, bob.ID, domaingroups.RoleManager); err != nil {
		t.Fatalf("promote bob: %v", err)
	}
	if role, ok, err := db.GroupMemberRole(ctx, g.ID, bob.ID); err != nil || !ok || role != domaingroups.RoleManager {
		t.Errorf("bob role = %q ok=%v err=%v", role, ok, err)
	}
	if err := db.RemoveGroupMember(ctx, g.ID, carol.ID); err != nil {
		t.Fatalf("remove carol: %v", err)
	}
	if err := db.RemoveGroupMember(ctx, g.ID, carol.ID); apperr.CodeOf(err) != apperr.CodeNotFound {
		t.Errorf("remove missing member = %v, want not_found", err)
	}

	if err := db.DeleteGroup(ctx, g.ID); err != nil {
		t.Fatalf("delete group: %v", err)
	}
	if _, err := db.GroupByID(ctx, g.ID); apperr.CodeOf(err) != apperr.CodeNotFound {
		t.Errorf("deleted group = %v, want not_found", err)
	}
}

// TestPurgeGroupNodeReleasesQuotaAndBlobs checks that a permanent group delete
// decrements the blob refcount and the owner's used_bytes in one transaction.
func TestPurgeGroupNodeReleasesQuotaAndBlobs(t *testing.T) {
	db, policyID := newTestDB(t)
	ctx := context.Background()
	alice := seedUser(t, db, "alice", "user")

	root, err := db.EnsureRoot(ctx, alice.ID, policyID)
	if err != nil {
		t.Fatalf("root: %v", err)
	}
	sha := strings.Repeat("cd", 32)
	if _, _, err := db.InsertBlob(ctx, sha, 100); err != nil {
		t.Fatalf("blob: %v", err)
	}
	blob, err := db.BlobBySHA(ctx, sha)
	if err != nil {
		t.Fatalf("blob by sha: %v", err)
	}
	g, groupRoot := createTestGroup(t, db, "清理", alice.ID, alice.ID)
	groupDir, err := db.CreateDir(ctx, access.UserGroupScope(alice.ID, false, []int64{g.ID}),
		groupRoot.ID, "docs", "docs")
	if err != nil {
		t.Fatalf("group dir: %v", err)
	}
	fileID := insertFileNode(t, db, alice.ID, nodeRef{ID: groupDir.ID, Path: groupDir.Path}, &g.ID, "a.bin", sha, 100)
	// A personal node keeps the blob alive so only the refcount drops.
	if err := db.IncRef(ctx, blob.ID); err != nil {
		t.Fatalf("inc ref: %v", err)
	}
	insertFileNode(t, db, alice.ID, nodeRef{ID: root.ID, Path: root.Path}, nil, "keep.bin", sha, 100)
	if _, err := db.ExecContext(ctx, `UPDATE users SET used_bytes = used_bytes + 100 WHERE id = ?`, alice.ID); err != nil {
		t.Fatalf("charge quota: %v", err)
	}

	sc := access.UserGroupScope(alice.ID, false, []int64{g.ID})
	node, err := db.NodeByID(ctx, sc, groupDir.ID)
	if err != nil {
		t.Fatalf("group dir: %v", err)
	}
	orphans, err := db.PurgeGroupNode(ctx, sc, node.ID, time.Now().UTC())
	if err != nil {
		t.Fatalf("purge group node: %v", err)
	}
	if len(orphans) != 0 {
		t.Errorf("orphans = %+v, want none (blob still referenced)", orphans)
	}
	if got := blobRefcount(t, db, sha); got != 1 {
		t.Errorf("blob refcount = %d, want 1", got)
	}
	if used := userUsedBytes(t, db, alice.ID); used != 0 {
		t.Errorf("used_bytes = %d, want 0", used)
	}
	if _, err := db.NodeByID(ctx, access.UserScope(alice.ID, false), fileID); apperr.CodeOf(err) != apperr.CodeNotFound {
		t.Errorf("group file survived purge: %v", err)
	}
	// The group root row itself is not counted as content, so the group is
	// empty again and can be deleted.
	if busy, err := db.GroupHasContent(ctx, g.ID); err != nil || busy {
		t.Errorf("group content after purge = %v, %v; want empty", busy, err)
	}
}

// TestPurgeUserTransfersGroupNodes checks P1's hand-over order: creator first,
// then the smallest remaining manager, then any member, then any admin.
func TestPurgeUserTransfersGroupNodes(t *testing.T) {
	db, _ := newTestDB(t)
	ctx := context.Background()
	alice := seedUser(t, db, "alice", "user")
	bob := seedUser(t, db, "bob", "user")
	admin := seedUser(t, db, "root", "admin")

	sha := strings.Repeat("ef", 32)
	if _, _, err := db.InsertBlob(ctx, sha, 10); err != nil {
		t.Fatalf("blob: %v", err)
	}
	g, root := createTestGroup(t, db, "交接", alice.ID, alice.ID)
	if err := db.AddGroupMember(ctx, g.ID, bob.ID, domaingroups.RoleManager, time.Now().UTC()); err != nil {
		t.Fatalf("add bob: %v", err)
	}
	nodeID := insertFileNode(t, db, alice.ID, root, &g.ID, "share.bin", sha, 10)

	// A second group where alice is the only member: the admin fallback must
	// pick a live owner.
	g2, root2 := createTestGroup(t, db, "兜底", alice.ID, alice.ID)
	nodeID2 := insertFileNode(t, db, alice.ID, root2, &g2.ID, "orphan.bin", sha, 10)

	if err := db.PurgeUserAccount(ctx, alice, time.Now().UTC(), time.Now().Add(30*24*time.Hour)); err != nil {
		t.Fatalf("purge alice: %v", err)
	}

	reowned, err := db.NodeByID(ctx, access.UserGroupScope(bob.ID, false, []int64{g.ID}), nodeID)
	if err != nil {
		t.Fatalf("transferred node: %v", err)
	}
	if reowned.OwnerID != bob.ID {
		t.Errorf("node owner = %d, want %d (remaining manager)", reowned.OwnerID, bob.ID)
	}
	reowned2, err := db.NodeByID(ctx, access.UserGroupScope(admin.ID, true, []int64{g2.ID}), nodeID2)
	if err != nil {
		t.Fatalf("fallback node: %v", err)
	}
	if reowned2.OwnerID != admin.ID {
		t.Errorf("fallback owner = %d, want admin %d", reowned2.OwnerID, admin.ID)
	}
	if g2After, err := db.GroupByID(ctx, g2.ID); err != nil {
		t.Errorf("group after purge: %v", err)
	} else if g2After.CreatedBy != nil {
		t.Errorf("created_by = %v, want NULL after purge", *g2After.CreatedBy)
	}
	if role, ok, err := db.GroupMemberRole(ctx, g.ID, bob.ID); err != nil || !ok || role != domaingroups.RoleManager {
		t.Errorf("bob role after purge = %q ok=%v err=%v", role, ok, err)
	}
	if _, err := db.UserByID(ctx, alice.ID); err == nil {
		t.Errorf("purged user still exists")
	}
}
