package access

import "testing"

func user(id int64, perms ...Action) Principal {
	return Principal{
		UserID: id,
		Role:   "user",
		Perms:  NewPermSet(perms...),
	}
}

func admin(perms ...Action) Principal {
	return Principal{UserID: 1, Role: "admin", Perms: NewPermSet(perms...)}
}

func node(id, owner int64) Resource {
	return Resource{Kind: ResourceNode, ID: id, OwnerID: owner}
}

func TestPolicyMatrix(t *testing.T) {
	tests := []struct {
		name   string
		p      Principal
		action Action
		res    Resource
		allow  bool
		reason string
	}{
		{"anonymous own none", AnonymousPrincipal(), ActionRead, node(1, 0), false, ReasonAnonymous},
		{"guest cannot read", user(2, ActionRead), ActionRead, node(1, 2), true, ReasonOwner},
		{"user own read", user(2, ActionRead), ActionRead, node(1, 2), true, ReasonOwner},
		{"user own create without perm", user(2), ActionCreate, node(1, 2), true, ReasonOwner},
		{"user other read", user(2, ActionRead), ActionRead, node(1, 3), false, ReasonNotOwner},
		{"admin other read no bypass", admin(ActionRead), ActionRead, node(1, 3), false, ReasonNotOwner},
		{"system allowed", SystemPrincipal(), ActionDelete, node(1, 3), true, ReasonSystem},
		{"system resource with perm", user(2, ActionAdminUsers), ActionAdminUsers, SystemResource(), true, ReasonPermission},
		{"system resource without perm", user(2, ActionRead), ActionAdminUsers, SystemResource(), false, ReasonPermissionDenied},
		{"anonymous system resource", AnonymousPrincipal(), ActionAdminUsers, SystemResource(), false, ReasonAnonymous},
		{"unknown action", user(2, ActionRead), Action("nuke"), node(1, 2), false, ReasonPermissionDenied},
		{"unknown resource kind", user(2, ActionRead), ActionRead, Resource{Kind: "weird"}, false, ReasonPermissionDenied},
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

func TestScopeValidate(t *testing.T) {
	valid := []Scope{
		UserScope(1, false),
		UserScope(1, true),
		{Anonymous: true},
		{System: true},
	}
	for _, sc := range valid {
		if err := sc.Validate(); err != nil {
			t.Errorf("Validate(%+v) unexpected error: %v", sc, err)
		}
	}
	invalid := []Scope{
		{},
		{UserID: 1, Anonymous: true},
		{UserID: 1, System: true},
		{Anonymous: true, System: true},
		{System: true, GroupIDs: []int64{1}},
	}
	for _, sc := range invalid {
		if err := sc.Validate(); err == nil {
			t.Errorf("Validate(%+v) = nil, want error", sc)
		}
	}
}
