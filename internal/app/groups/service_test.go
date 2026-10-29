package groups

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"rapig/internal/domain/access"
	domaingroups "rapig/internal/domain/groups"
	domainidentity "rapig/internal/domain/identity"
	"rapig/internal/infra/sql"
	"rapig/internal/platform/apperr"
	"rapig/migrations"
)

// userPerms is the permission set of an ordinary active account: every file
// action, but no admin.* capability.
func userPerms() access.PermSet {
	return access.NewPermSet(access.ActionRead, access.ActionWrite, access.ActionCreate,
		access.ActionDelete, access.ActionShare, access.ActionUpload)
}

// adminPerms is a system admin: file actions plus every admin capability.
func adminPerms() access.PermSet {
	return access.NewPermSet(access.AllActions...)
}

type groupEnv struct {
	svc   *Service
	db    *sqlstore.DB
	alice access.Principal // delegated creator (can_create_groups)
	bob   access.Principal // delegated creator (can_create_groups)
	carol access.Principal // plain user, no group capability
	root  access.Principal // system admin
}

func newGroupEnv(t *testing.T) *groupEnv {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	db, err := sqlstore.Open(ctx, filepath.Join(dir, "rapig.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Migrate(ctx, migrations.FS); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if _, err := db.EnsureDefaultStoragePolicy(ctx, filepath.Join(dir, "storage")); err != nil {
		t.Fatalf("policy: %v", err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	newUser := func(username, roleCode string, canCreate bool) access.Principal {
		roleID, err := db.RoleIDByCode(ctx, roleCode)
		if err != nil {
			t.Fatalf("role %s: %v", roleCode, err)
		}
		u, err := db.CreateUser(ctx, domainidentity.User{
			Username: username, UsernameFolded: username, DisplayName: username,
			PasswordHash: "x", RoleID: roleID, Status: domainidentity.StatusActive,
		})
		if err != nil {
			t.Fatalf("create user %s: %v", username, err)
		}
		if canCreate {
			flag := true
			if err := db.UpdateUser(ctx, u.ID, nil, nil, nil, &flag); err != nil {
				t.Fatalf("grant can_create_groups: %v", err)
			}
		}
		perms := userPerms()
		if roleCode == "admin" {
			perms = adminPerms()
		}
		return access.Principal{
			UserID: u.ID, Username: u.Username, Role: roleCode,
			Perms: perms, CanCreateGroups: canCreate,
		}
	}

	return &groupEnv{
		svc:   NewService(db, log),
		db:    db,
		alice: newUser("alice", "user", true),
		bob:   newUser("bob", "user", true),
		carol: newUser("carol", "user", false),
		root:  newUser("root", "admin", true),
	}
}

// TestCreateGroupLifecycle covers the happy path plus the validation rules from
// P1 §2: default member perms, name folding for conflicts, and the permission
// subset check.
func TestCreateGroupLifecycle(t *testing.T) {
	env := newGroupEnv(t)
	ctx := context.Background()

	g, rootID, err := env.svc.CreateGroup(ctx, env.alice, CreateRequest{Name: "  团队  "})
	if err != nil {
		t.Fatalf("create group: %v", err)
	}
	if g.Name != "团队" || g.NameFolded != "团队" {
		t.Errorf("group name = %q folded = %q", g.Name, g.NameFolded)
	}
	if g.CreatedBy == nil || *g.CreatedBy != env.alice.UserID {
		t.Errorf("created_by = %v, want %d", g.CreatedBy, env.alice.UserID)
	}
	if len(g.MemberPerms) != 1 || g.MemberPerms[0] != access.ActionRead {
		t.Errorf("default member perms = %v, want [read]", g.MemberPerms)
	}
	if rootID <= 0 {
		t.Fatalf("root id = %d", rootID)
	}
	root, err := env.db.NodeByID(ctx, access.UserGroupScope(env.alice.UserID, false, []int64{g.ID}), rootID)
	if err != nil {
		t.Fatalf("load group root: %v", err)
	}
	if root.GroupID == nil || *root.GroupID != g.ID {
		t.Errorf("root group_id = %v, want %d", root.GroupID, g.ID)
	}
	if !root.IsRoot() || root.Kind != "dir" {
		t.Errorf("root = kind %q parent %v", root.Kind, root.ParentID)
	}
	if root.OwnerID != env.alice.UserID {
		t.Errorf("root owner = %d, want creator %d", root.OwnerID, env.alice.UserID)
	}
	if role, ok, err := env.db.GroupMemberRole(ctx, g.ID, env.alice.UserID); err != nil || !ok || role != domaingroups.RoleManager {
		t.Errorf("creator role = %q ok=%v err=%v", role, ok, err)
	}

	// Duplicate names are compared on the folded (case/space insensitive) value.
	if _, _, err := env.svc.CreateGroup(ctx, env.bob, CreateRequest{Name: " 团队 "}); apperr.CodeOf(err) != apperr.CodeNameConflict {
		t.Errorf("duplicate group = %v, want name_conflict", err)
	}
	if _, _, err := env.svc.CreateGroup(ctx, env.alice, CreateRequest{Name: "   "}); apperr.CodeOf(err) != apperr.CodeInvalidName {
		t.Errorf("blank name = %v, want invalid_name", err)
	}

	// member_perms must be a non-empty subset of the file actions containing read.
	for name, perms := range map[string][]access.Action{
		"missing read": {access.ActionWrite, access.ActionUpload},
		"admin action": {access.ActionRead, access.ActionAdminUsers},
		"unknown":      {access.Action("nuke")},
	} {
		if _, _, err := env.svc.CreateGroup(ctx, env.alice, CreateRequest{Name: "权限 " + name, MemberPerms: perms}); apperr.CodeOf(err) != apperr.CodeBadRequest {
			t.Errorf("%s = %v, want 400", name, err)
		}
	}

	explicit, _, err := env.svc.CreateGroup(ctx, env.bob, CreateRequest{
		Name:        "协作",
		MemberPerms: []access.Action{access.ActionUpload, access.ActionRead},
	})
	if err != nil {
		t.Fatalf("create with explicit perms: %v", err)
	}
	if len(explicit.MemberPerms) != 2 || explicit.MemberPerms[0] != access.ActionRead ||
		explicit.MemberPerms[1] != access.ActionUpload {
		t.Errorf("member perms = %v, want normalized [read upload]", explicit.MemberPerms)
	}

	// A plain account without the capability cannot create groups at all.
	if _, _, err := env.svc.CreateGroup(ctx, env.carol, CreateRequest{Name: "无权限"}); apperr.CodeOf(err) != apperr.CodeForbidden {
		t.Errorf("plain user create = %v, want forbidden", err)
	}
}

// TestCreateGroupManagerDelegation: only a system admin may build a group for
// somebody else; a delegated user cannot hand out management.
func TestCreateGroupManagerDelegation(t *testing.T) {
	env := newGroupEnv(t)
	ctx := context.Background()

	managerID := env.bob.UserID
	g, _, err := env.svc.CreateGroup(ctx, env.root, CreateRequest{Name: "代建", ManagerID: &managerID})
	if err != nil {
		t.Fatalf("admin create for bob: %v", err)
	}
	if role, ok, err := env.db.GroupMemberRole(ctx, g.ID, env.bob.UserID); err != nil || !ok || role != domaingroups.RoleManager {
		t.Errorf("bob role = %q ok=%v err=%v", role, ok, err)
	}
	if _, ok, err := env.db.GroupMemberRole(ctx, g.ID, env.root.UserID); err != nil || ok {
		t.Errorf("admin should not become a member, ok=%v err=%v", ok, err)
	}
	if g.CreatedBy == nil || *g.CreatedBy != env.root.UserID {
		t.Errorf("created_by = %v, want admin %d", g.CreatedBy, env.root.UserID)
	}

	if _, _, err := env.svc.CreateGroup(ctx, env.alice, CreateRequest{Name: "越权指派", ManagerID: &env.carol.UserID}); apperr.CodeOf(err) != apperr.CodeForbidden {
		t.Errorf("delegated user delegation = %v, want forbidden", err)
	}
	missing := int64(987654)
	if _, _, err := env.svc.CreateGroup(ctx, env.root, CreateRequest{Name: "幽灵", ManagerID: &missing}); apperr.CodeOf(err) != apperr.CodeBadRequest {
		t.Errorf("missing manager = %v, want bad_request", err)
	}
}

// TestUpdateGroupAndDelegationScope: renaming, member_perms replacement, and the
// 404 invisibility rule for groups created by somebody else.
func TestUpdateGroupAndDelegationScope(t *testing.T) {
	env := newGroupEnv(t)
	ctx := context.Background()

	aliceGroup, _, err := env.svc.CreateGroup(ctx, env.alice, CreateRequest{Name: "Alice 组"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	bobGroup, _, err := env.svc.CreateGroup(ctx, env.bob, CreateRequest{Name: "Bob 组"})
	if err != nil {
		t.Fatalf("create bob: %v", err)
	}

	renamed := "Alice 新组"
	updated, err := env.svc.UpdateGroup(ctx, env.alice, aliceGroup.ID, UpdateRequest{
		Name:        &renamed,
		MemberPerms: []access.Action{access.ActionRead, access.ActionWrite},
	})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if updated.Name != renamed || len(updated.MemberPerms) != 2 {
		t.Errorf("updated = %+v", updated)
	}

	// Renaming onto another group's folded name is a conflict.
	if _, err := env.svc.UpdateGroup(ctx, env.alice, aliceGroup.ID, UpdateRequest{Name: &bobGroup.Name}); apperr.CodeOf(err) != apperr.CodeNameConflict {
		t.Errorf("rename conflict = %v, want name_conflict", err)
	}
	if _, err := env.svc.UpdateGroup(ctx, env.alice, aliceGroup.ID, UpdateRequest{Name: strPtr("  ")}); apperr.CodeOf(err) != apperr.CodeInvalidName {
		t.Errorf("blank rename = %v, want invalid_name", err)
	}
	if _, err := env.svc.UpdateGroup(ctx, env.alice, aliceGroup.ID, UpdateRequest{
		MemberPerms: []access.Action{access.ActionWrite},
	}); apperr.CodeOf(err) != apperr.CodeBadRequest {
		t.Errorf("perms without read = %v, want 400", err)
	}

	// Bob may not even see Alice's group: everything maps to 404.
	other := "偷改"
	if _, err := env.svc.UpdateGroup(ctx, env.bob, aliceGroup.ID, UpdateRequest{Name: &other}); apperr.CodeOf(err) != apperr.CodeNotFound {
		t.Errorf("cross-creator update = %v, want not_found", err)
	}
	if _, err := env.svc.ListMembers(ctx, env.bob, aliceGroup.ID); apperr.CodeOf(err) != apperr.CodeNotFound {
		t.Errorf("cross-creator members = %v, want not_found", err)
	}
	if err := env.svc.DeleteGroup(ctx, env.bob, aliceGroup.ID); apperr.CodeOf(err) != apperr.CodeNotFound {
		t.Errorf("cross-creator delete = %v, want not_found", err)
	}

	// The manage list is scoped per creator for delegated users, global for admin.
	aliceList, err := env.svc.ListGroups(ctx, env.alice)
	if err != nil || len(aliceList) != 1 || aliceList[0].ID != aliceGroup.ID {
		t.Errorf("alice groups = %+v, %v", aliceList, err)
	}
	adminList, err := env.svc.ListGroups(ctx, env.root)
	if err != nil || len(adminList) != 2 {
		t.Errorf("admin groups = %d, %v; want 2", len(adminList), err)
	}
	if _, err := env.svc.ListGroups(ctx, env.carol); apperr.CodeOf(err) != apperr.CodeForbidden {
		t.Errorf("plain user list = %v, want forbidden", err)
	}

	// The system admin manages any group.
	adminRenamed := "Admin 改名"
	if _, err := env.svc.UpdateGroup(ctx, env.root, bobGroup.ID, UpdateRequest{Name: &adminRenamed}); err != nil {
		t.Errorf("admin rename: %v", err)
	}
}

// TestDeleteGroupProtection: a group must be completely idle before it can be
// removed, and deletion cascades members and the root node.
func TestDeleteGroupProtection(t *testing.T) {
	env := newGroupEnv(t)
	ctx := context.Background()

	g, rootID, err := env.svc.CreateGroup(ctx, env.alice, CreateRequest{Name: "删除保护"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	sc := access.UserGroupScope(env.alice.UserID, false, []int64{g.ID})
	if _, err := env.db.CreateDir(ctx, sc, rootID, "sub", "sub"); err != nil {
		t.Fatalf("create dir: %v", err)
	}
	if err := env.svc.DeleteGroup(ctx, env.alice, g.ID); apperr.CodeOf(err) != apperr.CodeInvalidState {
		t.Errorf("delete non-empty = %v, want 409", err)
	}
	if err := env.svc.DeleteGroup(ctx, env.alice, g.ID); errReason(err) != apperr.ReasonGroupNotEmpty {
		t.Errorf("reason = %q, want group_not_empty", errReason(err))
	}

	// Clear the tree (the transport does this through PurgeGroupNode) and the
	// group becomes deletable again.
	if _, err := env.db.ExecContext(ctx, `DELETE FROM nodes WHERE group_id = ? AND parent_id IS NOT NULL`, g.ID); err != nil {
		t.Fatalf("clear nodes: %v", err)
	}
	// An in-flight upload keeps the group busy even with no nodes.
	now := time.Now().UTC().Unix()
	if _, err := env.db.ExecContext(ctx, `
		INSERT INTO uploads (id, owner_id, parent_id, storage_policy_id, name, name_folded, size, sha256,
			part_size, parts_total, received_bytes, state, created_at, expires_at)
		VALUES ('u1', ?, ?, (SELECT id FROM storage_policies WHERE is_default = 1), 'x', 'x', 1, ?, 1, 1, 0, 'open', ?, ?)`,
		env.alice.UserID, rootID, "aa", now, now+3600); err != nil {
		t.Fatalf("insert upload: %v", err)
	}
	if err := env.svc.DeleteGroup(ctx, env.alice, g.ID); errReason(err) != apperr.ReasonGroupNotEmpty {
		t.Errorf("delete with open upload = %v, want group_not_empty", err)
	}
	if _, err := env.db.ExecContext(ctx, `DELETE FROM uploads WHERE id = 'u1'`); err != nil {
		t.Fatalf("delete upload: %v", err)
	}

	if err := env.svc.DeleteGroup(ctx, env.alice, g.ID); err != nil {
		t.Fatalf("delete empty group: %v", err)
	}
	if _, err := env.db.GroupByID(ctx, g.ID); apperr.CodeOf(err) != apperr.CodeNotFound {
		t.Errorf("group after delete = %v, want not_found", err)
	}
	var members, roots int
	if err := env.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM group_members WHERE group_id = ?`, g.ID).Scan(&members); err != nil {
		t.Fatalf("count members: %v", err)
	}
	if err := env.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM nodes WHERE group_id = ?`, g.ID).Scan(&roots); err != nil {
		t.Fatalf("count nodes: %v", err)
	}
	if members != 0 || roots != 0 {
		t.Errorf("after delete members=%d nodes=%d, want cascade to 0", members, roots)
	}
}

// TestMembersAndLastManagerGuard keeps at least one manager alive and verifies
// the membership views members see.
func TestMembersAndLastManagerGuard(t *testing.T) {
	env := newGroupEnv(t)
	ctx := context.Background()

	g, _, err := env.svc.CreateGroup(ctx, env.alice, CreateRequest{Name: "成员"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := env.svc.RemoveMember(ctx, env.alice, g.ID, env.alice.UserID); apperr.CodeOf(err) != apperr.CodeInvalidState {
		t.Errorf("remove last manager = %v, want 409", err)
	}
	if err := env.svc.UpdateMemberRole(ctx, env.alice, g.ID, env.alice.UserID, domaingroups.RoleMember); errReason(err) != apperr.ReasonLastGroupManager {
		t.Errorf("demote last manager = %v, want last_group_manager", err)
	}

	m, created, err := env.svc.AddMember(ctx, env.alice, g.ID, env.carol.UserID, "")
	if err != nil {
		t.Fatalf("add member: %v", err)
	}
	if !created {
		t.Errorf("first add did not report creation")
	}
	if m.Role != domaingroups.RoleMember || m.Username != "carol" || m.DisplayName != "carol" {
		t.Errorf("member = %+v", m)
	}
	// 重复添加是幂等的；换角色则原地更新（AddMember 即成员角色入口）。
	m, created, err = env.svc.AddMember(ctx, env.alice, g.ID, env.carol.UserID, "")
	if err != nil || created || m.Role != domaingroups.RoleMember {
		t.Errorf("repeat add = %+v created=%v err=%v", m, created, err)
	}
	m, created, err = env.svc.AddMember(ctx, env.alice, g.ID, env.carol.UserID, domaingroups.RoleManager)
	if err != nil || created || m.Role != domaingroups.RoleManager {
		t.Errorf("promote via add = %+v created=%v err=%v", m, created, err)
	}
	if _, _, err := env.svc.AddMember(ctx, env.alice, g.ID, 987654, ""); apperr.CodeOf(err) != apperr.CodeNotFound {
		t.Errorf("missing user = %v, want not_found", err)
	}
	if _, _, err := env.svc.AddMember(ctx, env.alice, g.ID, env.bob.UserID, "owner"); apperr.CodeOf(err) != apperr.CodeBadRequest {
		t.Errorf("bad role = %v, want 400", err)
	}

	// Carol sees the group through MyGroups.
	my, err := env.svc.ListMyGroups(ctx, env.carol)
	if err != nil || len(my) != 1 || my[0].ID != g.ID {
		t.Errorf("carol groups = %+v, %v", my, err)
	}

	// Promote carol so Alice can step down, then the guard applies to Carol.
	if err := env.svc.UpdateMemberRole(ctx, env.alice, g.ID, env.carol.UserID, domaingroups.RoleManager); err != nil {
		t.Fatalf("promote carol: %v", err)
	}
	if err := env.svc.RemoveMember(ctx, env.alice, g.ID, env.alice.UserID); err != nil {
		t.Fatalf("remove alice: %v", err)
	}
	if err := env.svc.RemoveMember(ctx, env.alice, g.ID, env.carol.UserID); errReason(err) != apperr.ReasonLastGroupManager {
		t.Errorf("remove carol = %v, want last_group_manager", err)
	}

	members, err := env.svc.ListMembers(ctx, env.alice, g.ID)
	if err != nil || len(members) != 1 || members[0].UserID != env.carol.UserID {
		t.Errorf("members = %+v, %v", members, err)
	}
}

func strPtr(s string) *string { return &s }

// errReason returns the machine readable reason of an apperr, or "" when err is
// not an apperr.
func errReason(err error) string {
	e, ok := apperr.From(err)
	if !ok {
		return ""
	}
	return e.Reason
}
