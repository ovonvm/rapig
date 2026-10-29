package access

import "testing"

// groupNode builds a node resource that lives in a group space. OwnerID is still
// filled so audit logs know who created the row, but it grants nothing.
func groupNode(id, owner, group int64) Resource {
	g := group
	return Resource{Kind: ResourceNode, ID: id, OwnerID: owner, GroupID: &g}
}

// groupPrincipal is a signed-in user who belongs to the groups in roles.
func groupPrincipal(userID int64, roles map[int64]GroupRoleInfo) Principal {
	return Principal{UserID: userID, Role: "user", Perms: NewPermSet(ActionRead), GroupRoles: roles}
}

func allFilePerms() PermSet {
	return NewPermSet(ActionRead, ActionCreate, ActionUpload, ActionWrite, ActionDelete, ActionShare)
}

// TestGroupPolicyMatrix is the P1 access matrix: a manager may do everything in
// the group, a member only what member_perms grants, and everyone else (including
// system admins without a membership row) is denied.
func TestGroupPolicyMatrix(t *testing.T) {
	const group = int64(7)
	manager := groupPrincipal(2, map[int64]GroupRoleInfo{
		group: {Role: GroupRoleManager, Perms: allFilePerms()},
	})
	reader := groupPrincipal(3, map[int64]GroupRoleInfo{
		group: {Role: GroupRoleMember, Perms: NewPermSet(ActionRead)},
	})
	writer := groupPrincipal(4, map[int64]GroupRoleInfo{
		group: {Role: GroupRoleMember, Perms: NewPermSet(ActionRead, ActionWrite)},
	})
	outsider := groupPrincipal(5, nil)
	otherGroup := groupPrincipal(6, map[int64]GroupRoleInfo{
		99: {Role: GroupRoleManager, Perms: allFilePerms()},
	})
	// A system admin has the admin.users permission but no membership row: P1
	// forbids a file-access side door, so the group check must still deny.
	adminOutsider := Principal{
		UserID: 1, Role: "admin",
		Perms:      NewPermSet(AllActions...),
		GroupRoles: nil,
	}

	tests := []struct {
		name   string
		p      Principal
		action Action
		res    Resource
		allow  bool
		reason string
	}{
		{"manager read", manager, ActionRead, groupNode(10, 2, group), true, ReasonGroupRole},
		{"manager create", manager, ActionCreate, groupNode(10, 2, group), true, ReasonGroupRole},
		{"manager upload", manager, ActionUpload, groupNode(10, 2, group), true, ReasonGroupRole},
		{"manager write", manager, ActionWrite, groupNode(10, 2, group), true, ReasonGroupRole},
		{"manager delete", manager, ActionDelete, groupNode(10, 2, group), true, ReasonGroupRole},
		{"manager share", manager, ActionShare, groupNode(10, 2, group), true, ReasonGroupRole},
		{"manager group node owned by someone else", manager, ActionWrite, groupNode(11, 3, group), true, ReasonGroupRole},

		{"member read", reader, ActionRead, groupNode(10, 2, group), true, ReasonGroupRole},
		{"member write denied", reader, ActionWrite, groupNode(10, 2, group), false, ReasonPermissionDenied},
		{"member create denied", reader, ActionCreate, groupNode(10, 2, group), false, ReasonPermissionDenied},
		{"member delete denied", reader, ActionDelete, groupNode(10, 2, group), false, ReasonPermissionDenied},
		{"member grant write", writer, ActionWrite, groupNode(10, 2, group), true, ReasonGroupRole},
		{"member grant delete still denied", writer, ActionDelete, groupNode(10, 2, group), false, ReasonPermissionDenied},

		{"non-member read denied", outsider, ActionRead, groupNode(10, 2, group), false, ReasonGroupRole},
		{"non-member share denied", outsider, ActionShare, groupNode(10, 2, group), false, ReasonGroupRole},
		{"member of another group denied", otherGroup, ActionRead, groupNode(10, 2, group), false, ReasonGroupRole},
		{"admin without membership denied", adminOutsider, ActionRead, groupNode(10, 2, group), false, ReasonGroupRole},

		{"system principal allowed", SystemPrincipal(), ActionDelete, groupNode(10, 2, group), true, ReasonSystem},
		{"anonymous group node denied", AnonymousPrincipal(), ActionRead, groupNode(10, 2, group), false, ReasonAnonymous},

		// A personal node keeps the old owner rule even when the principal has
		// group roles; the group map must not leak into the personal space.
		{"personal node owner", manager, ActionRead, node(12, 2), true, ReasonOwner},
		{"personal node foreign", manager, ActionRead, node(12, 3), false, ReasonNotOwner},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := Check(tc.p, tc.action, tc.res)
			if got.Allow != tc.allow || got.Reason != tc.reason {
				t.Fatalf("Check() = %+v, want allow=%v reason=%s", got, tc.allow, tc.reason)
			}
		})
	}
}

// TestScopeForGroupIDs checks that the principal's memberships travel into the
// repository scope, and that the group ids cannot be smuggled into a system or
// anonymous scope.
func TestScopeForGroupIDs(t *testing.T) {
	p := groupPrincipal(2, map[int64]GroupRoleInfo{
		5: {Role: GroupRoleManager, Perms: allFilePerms()},
		3: {Role: GroupRoleMember, Perms: NewPermSet(ActionRead)},
	})
	sc := ScopeFor(p)
	if sc.UserID != 2 || sc.Admin || sc.Anonymous || sc.System {
		t.Fatalf("ScopeFor() = %+v", sc)
	}
	if len(sc.GroupIDs) != 2 || sc.GroupIDs[0] != 3 || sc.GroupIDs[1] != 5 {
		t.Errorf("ScopeFor group ids = %v, want [3 5] sorted", sc.GroupIDs)
	}
	if err := sc.Validate(); err != nil {
		t.Errorf("scope with groups must validate: %v", err)
	}
	if err := UserGroupScope(2, false, []int64{5, 5, 0, -1, 3}).Validate(); err != nil {
		t.Errorf("UserGroupScope must normalize instead of failing: %v", err)
	}
	if got := UserGroupScope(2, false, []int64{5, 5, 0, 3}).GroupIDs; len(got) != 2 || got[0] != 3 || got[1] != 5 {
		t.Errorf("UserGroupScope group ids = %v, want [3 5]", got)
	}
	if err := (Scope{System: true, GroupIDs: []int64{1}}).Validate(); err == nil {
		t.Error("system scope with group ids must be rejected")
	}
	if err := (Scope{Anonymous: true, GroupIDs: []int64{1}}).Validate(); err == nil {
		t.Error("anonymous scope with group ids must be rejected")
	}
}
