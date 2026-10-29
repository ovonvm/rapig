package httpx

import (
	"fmt"
	"net/http"
	"testing"
)

type adminUserT struct {
	ID         int64  `json:"id"`
	Username   string `json:"username"`
	Role       string `json:"role"`
	Status     string `json:"status"`
	QuotaBytes int64  `json:"quota_bytes"`
	// Identifiers are masked in the admin console; IsRoot marks the root admin.
	Email  string `json:"email"`
	Phone  string `json:"phone"`
	IsRoot bool   `json:"is_root"`
}

type adminUsersT struct {
	Items  []adminUserT `json:"items"`
	Total  int          `json:"total"`
	Limit  int          `json:"limit"`
	Offset int          `json:"offset"`
}

type adminAuditItemT struct {
	Action    string `json:"action"`
	ActorName string `json:"actor_name"`
	Result    string `json:"result"`
}

type adminAuditT struct {
	Items []adminAuditItemT `json:"items"`
	Total int               `json:"total"`
}

type adminSettingsT struct {
	SiteName            string `json:"site_name"`
	RegistrationEnabled bool   `json:"registration_enabled"`
	DefaultQuotaBytes   int64  `json:"default_quota_bytes"`
	AuditRetentionDays  int    `json:"audit_retention_days"`
	TrashRetentionDays  int    `json:"trash_retention_days"`
}

func TestAdminUserManagement(t *testing.T) {
	env := newTestEnv(t)
	c := env.client()
	c.login("admin", env.password)

	resp, body := c.do("GET", "/api/v1/admin/users?q=admin", nil, false)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list: %d %s", resp.StatusCode, body)
	}
	page := decodeJSONT[adminUsersT](t, body)
	if page.Total != 1 || len(page.Items) != 1 || page.Items[0].Role != "admin" {
		t.Fatalf("unexpected admin list: %+v", page)
	}
	adminID := page.Items[0].ID

	// Create bob with an explicit password.
	resp, body = c.do("POST", "/api/v1/admin/users", map[string]any{
		"username": "bob", "password": "bob-password-1", "role": "user", "quota_bytes": 1000,
	}, true)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create bob: %d %s", resp.StatusCode, body)
	}
	var created struct {
		User adminUserT `json:"user"`
	}
	created = decodeJSONT[struct {
		User adminUserT `json:"user"`
	}](t, body)
	if created.User.Username != "bob" || created.User.QuotaBytes != 1000 {
		t.Fatalf("unexpected created user: %+v", created)
	}

	// Non-admin is not told the admin API exists.
	bc := env.client()
	bc.login("bob", "bob-password-1")
	for _, path := range []string{"/api/v1/admin/users", "/api/v1/admin/audit", "/api/v1/admin/settings"} {
		resp, _ := bc.do("GET", path, nil, false)
		if resp.StatusCode != http.StatusNotFound {
			t.Fatalf("%s for bob: want 404, got %d", path, resp.StatusCode)
		}
	}

	// Update quota and role.
	resp, body = c.do("PATCH", fmt.Sprintf("/api/v1/admin/users/%d", created.User.ID),
		map[string]any{"quota_bytes": 2000, "role": "admin"}, true)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("patch user: %d %s", resp.StatusCode, body)
	}
	var patched struct {
		User adminUserT `json:"user"`
	}
	patched = decodeJSONT[struct {
		User adminUserT `json:"user"`
	}](t, body)
	if patched.User.QuotaBytes != 2000 || patched.User.Role != "admin" {
		t.Fatalf("patch not applied: %+v", patched)
	}

	// Self-disable is refused.
	resp, body = c.do("PATCH", fmt.Sprintf("/api/v1/admin/users/%d", adminID),
		map[string]any{"status": "disabled"}, true)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("self disable: want 409, got %d %s", resp.StatusCode, body)
	}
	if reason := errorReasonT(t, body); reason != "self_target" {
		t.Fatalf("want self_target, got %q", reason)
	}

	// Reset bob's password: old one stops working, new one works once.
	resp, body = c.do("POST", fmt.Sprintf("/api/v1/admin/users/%d/reset-password", created.User.ID), nil, true)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("reset: %d %s", resp.StatusCode, body)
	}
	var reset struct {
		Password string `json:"password"`
	}
	reset = decodeJSONT[struct {
		Password string `json:"password"`
	}](t, body)
	if len(reset.Password) < 16 {
		t.Fatalf("short reset password: %q", reset.Password)
	}
	old := env.client()
	old.do("GET", "/health", nil, false)
	if resp, _ := old.do("POST", "/api/v1/auth/login", map[string]string{"username": "bob", "password": "bob-password-1"}, true); resp.StatusCode == http.StatusOK {
		t.Fatal("old password still works after reset")
	}
	fresh := env.client()
	fresh.do("GET", "/health", nil, false)
	resp, body = fresh.do("POST", "/api/v1/auth/login", map[string]string{"username": "bob", "password": reset.Password}, true)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("new password rejected: %d %s", resp.StatusCode, body)
	}
}

func TestAdminAuditAndSettings(t *testing.T) {
	env := newTestEnv(t)
	c := env.client()
	c.login("admin", env.password)

	// Make sure at least one admin action exists.
	resp, body := c.do("POST", "/api/v1/admin/users", map[string]any{"username": "carol"}, true)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create carol: %d %s", resp.StatusCode, body)
	}

	resp, body = c.do("GET", "/api/v1/admin/audit?limit=50", nil, false)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("audit: %d %s", resp.StatusCode, body)
	}
	auditPage := decodeJSONT[adminAuditT](t, body)
	if auditPage.Total == 0 {
		t.Fatal("expected audit rows")
	}

	resp, body = c.do("GET", "/api/v1/admin/audit?action=admin.user_create", nil, false)
	filtered := decodeJSONT[adminAuditT](t, body)
	for _, item := range filtered.Items {
		if item.Action != "admin.user_create" {
			t.Fatalf("filter leaked %q", item.Action)
		}
	}
	resp, body = c.do("GET", "/api/v1/admin/audit/actions", nil, false)
	var actions struct {
		Actions []string `json:"actions"`
	}
	actions = decodeJSONT[struct {
		Actions []string `json:"actions"`
	}](t, body)
	found := false
	for _, a := range actions.Actions {
		if a == "admin.user_create" {
			found = true
		}
	}
	if !found {
		t.Fatalf("actions missing admin.user_create: %+v", actions)
	}

	resp, body = c.do("GET", "/api/v1/admin/settings", nil, false)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("settings: %d %s", resp.StatusCode, body)
	}
	settings := decodeJSONT[adminSettingsT](t, body)
	if settings.SiteName != "Rapig" || settings.RegistrationEnabled {
		t.Fatalf("unexpected defaults: %+v", settings)
	}

	resp, body = c.do("PUT", "/api/v1/admin/settings", map[string]any{
		"site_name": "我的网盘", "registration_enabled": true, "default_quota_bytes": 1_000_000_000,
	}, true)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("put settings: %d %s", resp.StatusCode, body)
	}
	settings = decodeJSONT[adminSettingsT](t, body)
	if settings.SiteName != "我的网盘" || !settings.RegistrationEnabled || settings.DefaultQuotaBytes != 1_000_000_000 {
		t.Fatalf("settings not applied: %+v", settings)
	}
	resp, body = c.do("PUT", "/api/v1/admin/settings", map[string]any{"site_name": ""}, true)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("empty site name: want 400, got %d %s", resp.StatusCode, body)
	}

	// Wrong method on a known admin route returns 405 with Allow.
	resp, _ = c.do("PUT", "/api/v1/admin/users", nil, true)
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("PUT /admin/users: want 405, got %d", resp.StatusCode)
	}
	if allow := resp.Header.Get("Allow"); allow != "GET, POST" {
		t.Fatalf("Allow = %q", allow)
	}
}

func errorReasonT(t *testing.T, body []byte) string {
	t.Helper()
	var payload struct {
		Error struct {
			Reason string `json:"reason"`
		} `json:"error"`
	}
	payload = decodeJSONT[struct {
		Error struct {
			Reason string `json:"reason"`
		} `json:"error"`
	}](t, body)
	return payload.Error.Reason
}
