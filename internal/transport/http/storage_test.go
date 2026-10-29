package httpx

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	domainaccess "rapig/internal/domain/access"
)

type storageListT struct {
	Items []struct {
		ID        int64  `json:"id"`
		Name      string `json:"name"`
		Type      string `json:"type"`
		RootPath  string `json:"root_path"`
		Enabled   bool   `json:"enabled"`
		IsDefault bool   `json:"is_default"`
		NodeCount int64  `json:"node_count"`
		FileCount int64  `json:"file_count"`
	} `json:"items"`
}

type userPoliciesT struct {
	Items []struct {
		ID        int64  `json:"id"`
		Name      string `json:"name"`
		IsDefault bool   `json:"is_default"`
		Enabled   bool   `json:"enabled"`
	} `json:"items"`
}

type nodeCreatedT struct {
	ID int64 `json:"id"`
}

type storagePolicyT struct {
	Policy struct {
		ID        int64  `json:"id"`
		Name      string `json:"name"`
		RootPath  string `json:"root_path"`
		Enabled   bool   `json:"enabled"`
		IsDefault bool   `json:"is_default"`
	} `json:"policy"`
}

// TestAdminStorageCRUD covers the management API: create/list/patch/delete,
// the guard rails (in-use, default, overlap, unknown id) and the admin-only
// visibility rule.
func TestAdminStorageCRUD(t *testing.T) {
	env := newTestEnv(t)
	c := env.client()
	c.login("admin", env.password)

	resp, body := c.do("GET", "/api/v1/admin/storage", nil, false)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list: %d %s", resp.StatusCode, body)
	}
	initial := decodeJSONT[storageListT](t, body)
	if len(initial.Items) != 1 || !initial.Items[0].IsDefault {
		t.Fatalf("initial policies: %+v", initial.Items)
	}
	defaultPolicy := initial.Items[0]

	// A non-admin sees 404, not 403: the admin API does not exist for them.
	createBob(t, c)
	bc := env.client()
	bc.login("bob", "bob-password-1")
	for _, tc := range []struct{ method, path string }{
		{"GET", "/api/v1/admin/storage"},
		{"POST", "/api/v1/admin/storage"},
		{"PATCH", "/api/v1/admin/storage/1"},
		{"DELETE", "/api/v1/admin/storage/1"},
	} {
		resp, _ := bc.do(tc.method, tc.path, map[string]any{"name": "x", "root_path": "C:/x"}, true)
		if resp.StatusCode != http.StatusNotFound {
			t.Fatalf("%s %s for bob: want 404, got %d", tc.method, tc.path, resp.StatusCode)
		}
	}

	// Create a second root.
	photosRoot := filepath.Join(t.TempDir(), "photos")
	resp, body = c.do("POST", "/api/v1/admin/storage", map[string]any{
		"name": "photos", "root_path": photosRoot,
	}, true)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create: %d %s", resp.StatusCode, body)
	}
	created := decodeJSONT[storagePolicyT](t, body)
	if created.Policy.Name != "photos" || created.Policy.IsDefault || !created.Policy.Enabled {
		t.Fatalf("created policy: %+v", created.Policy)
	}
	photosID := created.Policy.ID

	resp, body = c.do("GET", "/api/v1/admin/storage", nil, false)
	list := decodeJSONT[storageListT](t, body)
	if len(list.Items) != 2 {
		t.Fatalf("list after create: %+v", list.Items)
	}

	// Duplicate name conflicts; an empty name is a bad request.
	resp, body = c.do("POST", "/api/v1/admin/storage", map[string]any{
		"name": "photos", "root_path": filepath.Join(t.TempDir(), "other"),
	}, true)
	if resp.StatusCode != http.StatusConflict || decodeAPIError(t, body).Error.Code != "name_conflict" {
		t.Fatalf("duplicate name: %d %s", resp.StatusCode, body)
	}
	resp, body = c.do("POST", "/api/v1/admin/storage", map[string]any{
		"name": "", "root_path": filepath.Join(t.TempDir(), "empty"),
	}, true)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("empty name: %d %s", resp.StatusCode, body)
	}

	// A root under an existing root overlaps it.
	resp, body = c.do("PATCH", fmt.Sprintf("/api/v1/admin/storage/%d", photosID), map[string]any{
		"root_path": filepath.Join(defaultPolicy.RootPath, "nested"),
	}, true)
	if resp.StatusCode != http.StatusConflict || decodeAPIError(t, body).Error.Reason != "path_overlap" {
		t.Fatalf("overlap: %d %s", resp.StatusCode, body)
	}

	// A root that cannot be created (a file is in the way) is not writable.
	file := filepath.Join(t.TempDir(), "plain-file")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	resp, body = c.do("POST", "/api/v1/admin/storage", map[string]any{
		"name": "broken", "root_path": filepath.Join(file, "sub"),
	}, true)
	if resp.StatusCode != http.StatusBadRequest || decodeAPIError(t, body).Error.Reason != "not_writable" {
		t.Fatalf("not writable: %d %s", resp.StatusCode, body)
	}

	// Rename and move to a fresh root.
	newRoot := filepath.Join(t.TempDir(), "photos2")
	resp, body = c.do("PATCH", fmt.Sprintf("/api/v1/admin/storage/%d", photosID), map[string]any{
		"name": "photos-renamed", "root_path": newRoot,
	}, true)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("patch: %d %s", resp.StatusCode, body)
	}
	patched := decodeJSONT[storagePolicyT](t, body)
	if patched.Policy.Name != "photos-renamed" || patched.Policy.RootPath != newRoot {
		t.Fatalf("patched policy: %+v", patched.Policy)
	}

	// Unknown id is 404.
	resp, _ = c.do("PATCH", "/api/v1/admin/storage/99999", map[string]any{"name": "x"}, true)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown patch: %d", resp.StatusCode)
	}

	// The default policy cannot be deleted.
	resp, body = c.do("DELETE", fmt.Sprintf("/api/v1/admin/storage/%d", defaultPolicy.ID), nil, true)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("delete default: %d %s", resp.StatusCode, body)
	}
	if apiErr := decodeAPIError(t, body); apiErr.Error.Code != "invalid_state" || apiErr.Error.Reason != "default_policy" {
		t.Fatalf("delete default error: %+v", apiErr)
	}

	// A policy holding nodes is in use.
	resp, body = c.do("POST", "/api/v1/nodes", map[string]any{
		"parent": 0, "name": "in-photos", "kind": "dir", "storage_policy_id": photosID,
	}, true)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create root dir in photos: %d %s", resp.StatusCode, body)
	}
	resp, body = c.do("DELETE", fmt.Sprintf("/api/v1/admin/storage/%d", photosID), nil, true)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("delete in-use: %d %s", resp.StatusCode, body)
	}
	if apiErr := decodeAPIError(t, body); apiErr.Error.Code != "invalid_state" || apiErr.Error.Reason != "storage_in_use" {
		t.Fatalf("delete in-use error: %+v", apiErr)
	}
}

// TestStoragePoliciesForUsers: any signed-in user can see the enabled roots,
// disabled ones stay hidden, and the list is ordered by id.
func TestStoragePoliciesForUsers(t *testing.T) {
	env := newTestEnv(t)

	resp, _ := env.client().do("GET", "/api/v1/storage/policies", nil, false)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("anonymous list: want 401, got %d", resp.StatusCode)
	}

	c := env.client()
	c.login("admin", env.password)
	resp, body := c.do("POST", "/api/v1/admin/storage", map[string]any{
		"name": "extra", "root_path": filepath.Join(t.TempDir(), "extra"),
	}, true)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create extra: %d %s", resp.StatusCode, body)
	}
	extraID := decodeJSONT[storagePolicyT](t, body).Policy.ID
	disabled, body := c.do("POST", "/api/v1/admin/storage", map[string]any{
		"name": "disabled", "root_path": filepath.Join(t.TempDir(), "disabled"), "enabled": false,
	}, true)
	if disabled.StatusCode != http.StatusCreated {
		t.Fatalf("create disabled: %d %s", disabled.StatusCode, body)
	}
	if decodeJSONT[storagePolicyT](t, body).Policy.Enabled {
		t.Fatal("disabled policy created as enabled")
	}

	createBob(t, c)
	bc := env.client()
	bc.login("bob", "bob-password-1")
	resp, body = bc.do("GET", "/api/v1/storage/policies", nil, false)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("bob list: %d %s", resp.StatusCode, body)
	}
	page := decodeJSONT[userPoliciesT](t, body)
	if len(page.Items) != 2 {
		t.Fatalf("bob sees %d policies, want 2 enabled ones: %+v", len(page.Items), page.Items)
	}
	if page.Items[0].ID != env.policyID || !page.Items[0].IsDefault {
		t.Fatalf("first item = %+v, want default policy %d", page.Items[0], env.policyID)
	}
	if page.Items[1].ID != extraID {
		t.Fatalf("second item = %+v, want %d", page.Items[1], extraID)
	}
	for _, item := range page.Items {
		if !item.Enabled {
			t.Fatalf("disabled policy leaked: %+v", item)
		}
	}

	// Disabling the extra policy hides it from users but not from admins.
	resp, body = c.do("PATCH", fmt.Sprintf("/api/v1/admin/storage/%d", extraID), map[string]any{"enabled": false}, true)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("disable extra: %d %s", resp.StatusCode, body)
	}
	_, body = bc.do("GET", "/api/v1/storage/policies", nil, false)
	page = decodeJSONT[userPoliciesT](t, body)
	if len(page.Items) != 1 || page.Items[0].ID != env.policyID {
		t.Fatalf("after disable: %+v", page.Items)
	}
}

// TestNodeCreationWithStoragePolicy: only a root-level folder may pick a
// policy; children inherit, and an explicit policy on a child is rejected.
func TestNodeCreationWithStoragePolicy(t *testing.T) {
	env := newTestEnv(t)
	c := env.client()
	c.login("admin", env.password)

	resp, body := c.do("POST", "/api/v1/admin/storage", map[string]any{
		"name": "second", "root_path": filepath.Join(t.TempDir(), "second"),
	}, true)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create policy: %d %s", resp.StatusCode, body)
	}
	secondID := decodeJSONT[storagePolicyT](t, body).Policy.ID

	resp, body = c.do("POST", "/api/v1/nodes", map[string]any{
		"parent": 0, "name": "second-root", "kind": "dir", "storage_policy_id": secondID,
	}, true)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create root dir: %d %s", resp.StatusCode, body)
	}
	root := decodeJSONT[nodeCreatedT](t, body)

	sc := domainaccess.UserScope(adminUserID(t, env), true)
	stored, err := env.db.NodeByID(context.Background(), sc, root.ID)
	if err != nil {
		t.Fatalf("load node: %v", err)
	}
	if stored.StoragePolicyID == nil || *stored.StoragePolicyID != secondID {
		t.Fatalf("root dir policy = %v, want %d", stored.StoragePolicyID, secondID)
	}

	// A child inherits the policy and must not pass one explicitly.
	resp, body = c.do("POST", "/api/v1/nodes", map[string]any{
		"parent": root.ID, "name": "child", "kind": "dir",
	}, true)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create child: %d %s", resp.StatusCode, body)
	}
	child := decodeJSONT[nodeCreatedT](t, body)
	stored, err = env.db.NodeByID(context.Background(), sc, child.ID)
	if err != nil {
		t.Fatalf("load child: %v", err)
	}
	if stored.StoragePolicyID == nil || *stored.StoragePolicyID != secondID {
		t.Fatalf("child policy = %v, want inherited %d", stored.StoragePolicyID, secondID)
	}

	resp, body = c.do("POST", "/api/v1/nodes", map[string]any{
		"parent": root.ID, "name": "explicit", "kind": "dir", "storage_policy_id": secondID,
	}, true)
	if resp.StatusCode != http.StatusBadRequest || decodeAPIError(t, body).Error.Code != "invalid_target" {
		t.Fatalf("explicit policy on child: %d %s", resp.StatusCode, body)
	}

	// Unknown or disabled policies are invalid targets too.
	resp, body = c.do("POST", "/api/v1/nodes", map[string]any{
		"parent": 0, "name": "ghost", "kind": "dir", "storage_policy_id": 987654,
	}, true)
	if resp.StatusCode != http.StatusBadRequest || decodeAPIError(t, body).Error.Code != "invalid_target" {
		t.Fatalf("unknown policy: %d %s", resp.StatusCode, body)
	}
	resp, body = c.do("PATCH", fmt.Sprintf("/api/v1/admin/storage/%d", secondID), map[string]any{"enabled": false}, true)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("disable second: %d %s", resp.StatusCode, body)
	}
	resp, body = c.do("POST", "/api/v1/nodes", map[string]any{
		"parent": 0, "name": "disabled-root", "kind": "dir", "storage_policy_id": secondID,
	}, true)
	if resp.StatusCode != http.StatusBadRequest || decodeAPIError(t, body).Error.Code != "invalid_target" {
		t.Fatalf("disabled policy: %d %s", resp.StatusCode, body)
	}
}

// createBob provisions a plain user through the admin API (the tests that need
// a non-admin session all use this).
func createBob(t *testing.T, c *apiClient) {
	t.Helper()
	resp, body := c.do("POST", "/api/v1/admin/users", map[string]any{
		"username": "bob", "password": "bob-password-1", "role": "user", "quota_bytes": 1 << 20,
	}, true)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create bob: %d %s", resp.StatusCode, body)
	}
}

// adminUserID resolves the bootstrapped admin's id without guessing.
func adminUserID(t *testing.T, env *testEnv) int64 {
	t.Helper()
	resp, body := env.client().do("GET", "/health", nil, false)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("health: %d %s", resp.StatusCode, body)
	}
	ac := env.client()
	ac.login("admin", env.password)
	resp, body = ac.do("GET", "/api/v1/auth/me", nil, false)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("me: %d %s", resp.StatusCode, body)
	}
	return decodeJSONT[meResponse](t, body).User.ID
}
