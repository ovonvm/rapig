package httpx

import (
	"net/http"
	"strings"
	"testing"
)

// ---- wire shapes for the P1 endpoints --------------------------------------

type groupT struct {
	ID          int64    `json:"id"`
	Name        string   `json:"name"`
	MemberPerms []string `json:"member_perms"`
	CreatedBy   *int64   `json:"created_by"`
	CreatedAt   string   `json:"created_at"`
}

type groupEnvelopeT struct {
	Group  groupT `json:"group"`
	RootID int64  `json:"root_id"`
}

type groupListT struct {
	Items []groupT `json:"items"`
}

type groupMemberT struct {
	UserID      int64  `json:"user_id"`
	Username    string `json:"username"`
	DisplayName string `json:"display_name"`
	Role        string `json:"role"`
}

type groupMemberListT struct {
	Items []groupMemberT `json:"items"`
}

type groupMemberEnvelopeT struct {
	Member groupMemberT `json:"member"`
}

type groupCreatedUserT struct {
	User struct {
		ID              int64  `json:"id"`
		Username        string `json:"username"`
		CanCreateGroups bool   `json:"can_create_groups"`
	} `json:"user"`
}

type meFlagT struct {
	User struct {
		ID              int64  `json:"id"`
		Username        string `json:"username"`
		CanCreateGroups bool   `json:"can_create_groups"`
	} `json:"user"`
}

type nodeFlagT struct {
	ID       int64  `json:"id"`
	ParentID int64  `json:"parent_id"`
	Kind     string `json:"kind"`
	Name     string `json:"name"`
	GroupID  *int64 `json:"group_id"`
}

// createTestUser makes an active user through the admin API and returns its id.
func createTestUser(t *testing.T, admin *apiClient, username, password string) int64 {
	t.Helper()
	resp, body := admin.do("POST", "/api/v1/admin/users", map[string]any{
		"username": username, "password": password, "role": "user",
	}, true)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create user %s: %d %s", username, resp.StatusCode, body)
	}
	return decodeJSONT[groupCreatedUserT](t, body).User.ID
}

// TestGroupEndpoints covers the 9 P1 endpoints end to end: management by a
// delegated creator, group file access, permanent deletion, and the empty-group
// deletion guard.
func TestGroupEndpoints(t *testing.T) {
	env := newTestEnv(t)
	admin := env.client()
	admin.login("admin", env.password)

	daveID := createTestUser(t, admin, "dave", "dave-password-1")
	erinID := createTestUser(t, admin, "erin", "erin-password-1")

	// Only admin.users may flip the delegation flag, and the flag surfaces in
	// both the admin view and /auth/me.
	resp, body := admin.do("PATCH", "/api/v1/admin/users/"+itoa(daveID),
		map[string]any{"can_create_groups": true}, true)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("grant flag: %d %s", resp.StatusCode, body)
	}
	if !decodeJSONT[groupCreatedUserT](t, body).User.CanCreateGroups {
		t.Fatalf("admin view lacks can_create_groups: %s", body)
	}

	dave := env.client()
	dave.login("dave", "dave-password-1")
	resp, body = dave.do("GET", "/api/v1/auth/me", nil, false)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("me: %d %s", resp.StatusCode, body)
	}
	if me := decodeJSONT[meFlagT](t, body); !me.User.CanCreateGroups {
		t.Errorf("delegated user is missing can_create_groups: %s", body)
	}

	erin := env.client()
	erin.login("erin", "erin-password-1")
	resp, body = erin.do("GET", "/api/v1/auth/me", nil, false)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("me erin: %d %s", resp.StatusCode, body)
	}
	if me := decodeJSONT[meFlagT](t, body); me.User.CanCreateGroups {
		t.Errorf("plain user must not have can_create_groups: %s", body)
	}

	// Anonymous access is rejected before any business logic.
	anon := env.client()
	if resp, _ := anon.do("GET", "/api/v1/groups", nil, false); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("anonymous list groups = %d, want 401", resp.StatusCode)
	}

	// POST /api/v1/groups: the delegated user creates a group and its root.
	resp, body = dave.do("POST", "/api/v1/groups", map[string]any{"name": "团队"}, true)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create group: %d %s", resp.StatusCode, body)
	}
	created := decodeJSONT[groupEnvelopeT](t, body)
	if created.Group.ID <= 0 || created.Group.Name != "团队" || created.RootID <= 0 {
		t.Fatalf("created group = %+v", created)
	}
	if len(created.Group.MemberPerms) != 1 || created.Group.MemberPerms[0] != "read" {
		t.Errorf("default member_perms = %v", created.Group.MemberPerms)
	}
	if created.Group.CreatedBy == nil || *created.Group.CreatedBy != daveID {
		t.Errorf("created_by = %v, want %d", created.Group.CreatedBy, daveID)
	}
	groupID := created.Group.ID
	rootID := created.RootID

	// A plain user cannot create groups; duplicates are 409.
	if resp, _ := erin.do("POST", "/api/v1/groups", map[string]any{"name": "偷建"}, true); resp.StatusCode != http.StatusForbidden {
		t.Errorf("plain user create = %d, want 403", resp.StatusCode)
	}
	resp, body = dave.do("POST", "/api/v1/groups", map[string]any{"name": "团队"}, true)
	if resp.StatusCode != http.StatusConflict || decodeAPIError(t, body).Error.Code != "name_conflict" {
		t.Errorf("duplicate group = %d %s", resp.StatusCode, body)
	}

	// GET /api/v1/groups is the membership view; the admin list is the
	// management view, restricted to self-created groups for delegated users.
	resp, body = dave.do("GET", "/api/v1/groups", nil, false)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list groups: %d %s", resp.StatusCode, body)
	}
	if got := decodeJSONT[groupListT](t, body); len(got.Items) != 1 || got.Items[0].ID != groupID {
		t.Errorf("dave groups = %+v", got)
	}
	if got := decodeJSONT[groupListT](t, mustDo(t, erin, "GET", "/api/v1/groups")); len(got.Items) != 0 {
		t.Errorf("erin groups = %+v, want none", got)
	}
	if got := decodeJSONT[groupListT](t, mustDo(t, admin, "GET", "/api/v1/admin/groups")); len(got.Items) != 1 {
		t.Errorf("admin groups = %+v, want 1", got)
	}
	if resp, body := erin.do("GET", "/api/v1/admin/groups", nil, false); resp.StatusCode != http.StatusForbidden {
		t.Errorf("plain user admin list = %d %s, want 403", resp.StatusCode, body)
	}

	// PATCH /api/v1/admin/groups/{id}: rename plus member perms.
	resp, body = dave.do("PATCH", "/api/v1/admin/groups/"+itoa(groupID), map[string]any{
		"name": "团队-改名", "member_perms": []string{"read", "upload"},
	}, true)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("update group: %d %s", resp.StatusCode, body)
	}
	updated := decodeJSONT[groupEnvelopeT](t, body)
	if updated.Group.Name != "团队-改名" || len(updated.Group.MemberPerms) != 2 {
		t.Errorf("updated group = %+v", updated.Group)
	}
	// member_perms must contain read and only file actions.
	resp, body = dave.do("PATCH", "/api/v1/admin/groups/"+itoa(groupID),
		map[string]any{"member_perms": []string{"write"}}, true)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("perms without read = %d %s, want 400", resp.StatusCode, body)
	}
	if err := decodeAPIError(t, body).Error.Reason; err != "group_perms_invalid" {
		t.Errorf("reason = %q, want group_perms_invalid", err)
	}
	// Another delegated user cannot even see the group: 404, not 403.
	if resp, body := admin.do("PATCH", "/api/v1/admin/users/"+itoa(erinID),
		map[string]any{"can_create_groups": true}, true); resp.StatusCode != http.StatusOK {
		t.Fatalf("grant flag to erin: %d %s", resp.StatusCode, body)
	}
	if resp, _ := erin.do("PATCH", "/api/v1/admin/groups/"+itoa(groupID),
		map[string]any{"name": "x"}, true); resp.StatusCode != http.StatusNotFound {
		t.Errorf("cross-user update = %d, want 404", resp.StatusCode)
	}

	// Members: add, list, and the last-manager guard.
	resp, body = dave.do("POST", "/api/v1/admin/groups/"+itoa(groupID)+"/members",
		map[string]any{"user_id": erinID}, true)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("add member: %d %s", resp.StatusCode, body)
	}
	if added := decodeJSONT[groupMemberEnvelopeT](t, body).Member; added.UserID != erinID || added.Role != "member" || added.Username != "erin" {
		t.Errorf("added member = %+v", added)
	}
	// Re-adding an existing member is a role update (200, not 201).
	resp, body = dave.do("POST", "/api/v1/admin/groups/"+itoa(groupID)+"/members",
		map[string]any{"user_id": erinID, "role": "member"}, true)
	if resp.StatusCode != http.StatusOK {
		t.Errorf("re-add member = %d %s, want 200", resp.StatusCode, body)
	}
	resp, body = dave.do("GET", "/api/v1/admin/groups/"+itoa(groupID)+"/members", nil, false)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list members: %d %s", resp.StatusCode, body)
	}
	members := decodeJSONT[groupMemberListT](t, body)
	if len(members.Items) != 2 || members.Items[0].Role != "manager" || members.Items[1].UserID != erinID {
		t.Errorf("members = %+v", members.Items)
	}
	resp, body = dave.do("DELETE", "/api/v1/admin/groups/"+itoa(groupID)+"/members/"+itoa(daveID), nil, true)
	if resp.StatusCode != http.StatusConflict {
		t.Errorf("remove last manager = %d %s, want 409", resp.StatusCode, body)
	}
	if reason := decodeAPIError(t, body).Error.Reason; reason != "last_group_manager" {
		t.Errorf("reason = %q, want last_group_manager", reason)
	}

	// Group space files: the manager may create, a read-only member may not.
	resp, body = dave.do("POST", "/api/v1/nodes", map[string]any{"parent": rootID, "name": "docs"}, true)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create group dir: %d %s", resp.StatusCode, body)
	}
	node := decodeJSONT[nodeFlagT](t, body)
	if node.GroupID == nil || *node.GroupID != groupID {
		t.Fatalf("node group_id = %v, want %d", node.GroupID, groupID)
	}
	dirID := node.ID
	if resp, body := erin.do("POST", "/api/v1/nodes", map[string]any{"parent": rootID, "name": "nope"}, true); resp.StatusCode != http.StatusNotFound {
		t.Errorf("read-only member create = %d %s, want 404", resp.StatusCode, body)
	}
	// A member with read may list the group root.
	if resp, body := erin.do("GET", "/api/v1/nodes?parent="+itoa(rootID), nil, false); resp.StatusCode != http.StatusOK {
		t.Errorf("member list = %d %s", resp.StatusCode, body)
	}
	// The admin has no file bypass outside membership.
	if resp, body := admin.do("GET", "/api/v1/nodes?parent="+itoa(rootID), nil, false); resp.StatusCode != http.StatusNotFound {
		t.Errorf("admin file bypass = %d %s, want 404", resp.StatusCode, body)
	}
	// Group files never enter the trash: DELETE is permanent and requires the
	// delete permission (a manager has it).
	if resp, body := erin.do("DELETE", "/api/v1/nodes/"+itoa(dirID), nil, true); resp.StatusCode != http.StatusNotFound {
		t.Errorf("member delete = %d %s, want 404", resp.StatusCode, body)
	}
	if resp, body := dave.do("DELETE", "/api/v1/nodes/"+itoa(dirID), nil, true); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("manager delete = %d %s", resp.StatusCode, body)
	}
	// Nothing landed in the personal trash of the deleter either.
	if resp, body := dave.do("GET", "/api/v1/trash", nil, false); resp.StatusCode != http.StatusOK ||
		strings.Contains(string(body), "docs") {
		t.Errorf("trash after group delete = %d %s", resp.StatusCode, body)
	}

	// DELETE /api/v1/admin/groups/{id}: empty groups only.
	if _, body := dave.do("POST", "/api/v1/nodes", map[string]any{"parent": rootID, "name": "keep"}, true); body == nil {
		t.Fatal("recreate dir failed")
	}
	resp, body = dave.do("DELETE", "/api/v1/admin/groups/"+itoa(groupID), nil, true)
	if resp.StatusCode != http.StatusConflict || decodeAPIError(t, body).Error.Reason != "group_not_empty" {
		t.Errorf("delete non-empty = %d %s", resp.StatusCode, body)
	}
	// Remove the file and try again.
	resp, body = dave.do("GET", "/api/v1/nodes?parent="+itoa(rootID), nil, false)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list group root: %d %s", resp.StatusCode, body)
	}
	list := decodeJSONT[listNodesResponseT](t, body)
	if len(list.Items) != 1 {
		t.Fatalf("group root items = %+v", list.Items)
	}
	if resp, body := dave.do("DELETE", "/api/v1/nodes/"+itoa(list.Items[0].ID), nil, true); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete keep = %d %s", resp.StatusCode, body)
	}
	resp, body = dave.do("DELETE", "/api/v1/admin/groups/"+itoa(groupID), nil, true)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete empty group = %d %s", resp.StatusCode, body)
	}
	if resp, _ := dave.do("GET", "/api/v1/admin/groups/"+itoa(groupID)+"/members", nil, false); resp.StatusCode != http.StatusNotFound {
		t.Errorf("members of deleted group = %d, want 404", resp.StatusCode)
	}
	if got := decodeJSONT[groupListT](t, mustDo(t, dave, "GET", "/api/v1/groups")); len(got.Items) != 0 {
		t.Errorf("groups after delete = %+v", got)
	}
}

// TestAdminGroupEndpoints covers the system-admin path: creating a group for
// somebody else, and renaming a group the admin did not create.
func TestAdminGroupEndpoints(t *testing.T) {
	env := newTestEnv(t)
	admin := env.client()
	admin.login("admin", env.password)

	bobID := createTestUser(t, admin, "bob", "bob-password-1")
	resp, body := admin.do("POST", "/api/v1/admin/groups", map[string]any{
		"name": "代建组", "manager_id": bobID, "member_perms": []string{"read", "upload"},
	}, true)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("admin create: %d %s", resp.StatusCode, body)
	}
	created := decodeJSONT[groupEnvelopeT](t, body)
	if created.Group.CreatedBy == nil || created.Group.MemberPerms[1] != "upload" {
		t.Errorf("created = %+v", created.Group)
	}
	if resp, body := admin.do("GET", "/api/v1/admin/groups/"+itoa(created.Group.ID)+"/members", nil, false); resp.StatusCode != http.StatusOK {
		t.Fatalf("members: %d %s", resp.StatusCode, body)
	} else if members := decodeJSONT[groupMemberListT](t, body); len(members.Items) != 1 ||
		members.Items[0].UserID != bobID || members.Items[0].Role != "manager" {
		t.Errorf("members = %+v", members.Items)
	}
	resp, body = admin.do("PATCH", "/api/v1/admin/groups/"+itoa(created.Group.ID),
		map[string]any{"name": "代建组-2"}, true)
	if resp.StatusCode != http.StatusOK {
		t.Errorf("admin rename: %d %s", resp.StatusCode, body)
	}
	if resp, body := admin.do("POST", "/api/v1/admin/groups", map[string]any{
		"name": "幽灵经理", "manager_id": 987654,
	}, true); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("missing manager = %d %s", resp.StatusCode, body)
	}
}

// TestSPAGroupRoute checks that the client route /groups/:id falls back to the
// SPA shell instead of 404ing.
func TestSPAGroupRoute(t *testing.T) {
	env := newTestEnv(t)
	c := env.client()
	for _, path := range []string{"/groups", "/groups/42"} {
		resp, body := c.do("GET", path, nil, false)
		if resp.StatusCode != http.StatusOK {
			t.Errorf("GET %s = %d %s", path, resp.StatusCode, body)
		}
		if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "text/html") {
			t.Errorf("GET %s content-type = %q, want html", path, ct)
		}
	}
}

// mustDo performs a request that must succeed and returns its body.
func mustDo(t *testing.T, c *apiClient, method, path string) []byte {
	t.Helper()
	resp, body := c.do(method, path, nil, false)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("%s %s = %d %s", method, path, resp.StatusCode, body)
	}
	return body
}

// TestGroupUploadLandsInGroupSpace verifies that an upload initialized under a
// group root inherits the group on the finished node, which is how the design
// keeps uploads/transfers free of extra columns.
func TestGroupUploadLandsInGroupSpace(t *testing.T) {
	env := newTestEnv(t)
	admin := env.client()
	admin.login("admin", env.password)

	daveID := createTestUser(t, admin, "dave", "dave-password-1")
	mustPatch(t, admin, daveID, map[string]any{"can_create_groups": true})

	dave := env.client()
	dave.login("dave", "dave-password-1")
	resp, body := dave.do("POST", "/api/v1/groups", map[string]any{"name": "team"}, true)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create group: %d %s", resp.StatusCode, body)
	}
	created := decodeJSONT[groupEnvelopeT](t, body)

	resp, body = dave.do("POST", "/api/v1/uploads",
		map[string]any{"parent": created.RootID, "name": "g.txt", "size": 5}, true)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("init in group: %d %s", resp.StatusCode, body)
	}
	init := decodeJSONT[initUploadT](t, body)
	resp, body = dave.doRaw("PUT", "/api/v1/uploads/"+init.UploadID+"/parts/0",
		[]byte("hello"), "application/octet-stream", true, nil)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("put part: %d %s", resp.StatusCode, body)
	}
	resp, body = dave.do("POST", "/api/v1/uploads/"+init.UploadID+"/complete",
		map[string]any{"parts": []map[string]any{{"part_no": 0, "sha256": shaHex([]byte("hello"))}}}, true)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("complete in group: %d %s", resp.StatusCode, body)
	}
	node := decodeJSONT[nodeFlagT](t, body)
	if node.GroupID == nil || *node.GroupID != created.Group.ID {
		t.Fatalf("uploaded node group = %v, want %d", node.GroupID, created.Group.ID)
	}

	_, body = dave.do("GET", "/api/v1/nodes?parent="+itoa(created.RootID), nil, false)
	listed := decodeJSONT[listNodesResponseT](t, body)
	if listed.Total != 1 || len(listed.Items) != 1 || listed.Items[0].ID != node.ID {
		t.Fatalf("group listing after upload = %+v", listed)
	}
	// The file is not part of the personal space.
	_, body = dave.do("GET", "/api/v1/nodes?parent=0", nil, false)
	if personal := decodeJSONT[listNodesResponseT](t, body); personal.Total != 0 {
		t.Fatalf("personal root unexpectedly lists group content: %+v", personal)
	}
}

func mustPatch(t *testing.T, c *apiClient, id int64, payload map[string]any) []byte {
	t.Helper()
	resp, body := c.do("PATCH", "/api/v1/admin/users/"+itoa(id), payload, true)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("patch user %d: %d %s", id, resp.StatusCode, body)
	}
	return body
}
