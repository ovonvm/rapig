package httpx

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

type deletionStatusT struct {
	Enabled    bool    `json:"enabled"`
	DeletedAt  *string `json:"deleted_at"`
	PurgeAfter *string `json:"purge_after"`
}

type deletionTicketT struct {
	CancelToken string `json:"cancel_token"`
	PurgeAt     string `json:"purge_at"`
}

type createdUserT struct {
	User adminUserT `json:"user"`
}

func TestAccountDeletionRequestAndCancel(t *testing.T) {
	env := newTestEnv(t)
	admin := env.client()
	admin.login("admin", env.password)

	// Create the account that will be deleted.
	resp, body := admin.do("POST", "/api/v1/admin/users", map[string]any{
		"username": "m52c", "password": "m52c-password-1", "role": "user",
	}, true)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create user: %d %s", resp.StatusCode, body)
	}
	created := decodeJSONT[createdUserT](t, body)

	user := env.client()
	user.login("m52c", "m52c-password-1")

	// The status endpoint is authenticated and reports the switch as closed.
	resp, body = user.do("GET", "/api/v1/auth/deletion", nil, false)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: %d %s", resp.StatusCode, body)
	}
	status := decodeJSONT[deletionStatusT](t, body)
	if status.Enabled || status.DeletedAt != nil || status.PurgeAfter != nil {
		t.Fatalf("unexpected status before enabling deletion: %+v", status)
	}
	if resp, _ := env.client().do("GET", "/api/v1/auth/deletion", nil, false); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("anonymous status: want 401, got %d", resp.StatusCode)
	}

	// Closed switch refuses the request.
	resp, body = user.do("POST", "/api/v1/auth/deletion/request", map[string]any{"password": "m52c-password-1"}, true)
	if resp.StatusCode != http.StatusForbidden || errorReasonT(t, body) != "deletion_disabled" {
		t.Fatalf("request while disabled: %d %s", resp.StatusCode, body)
	}

	// Only root may flip the switch: an admin (not root) changing the value 403s.
	resp, body = admin.do("POST", "/api/v1/admin/users", map[string]any{
		"username": "ops", "password": "ops-password-123", "role": "admin",
	}, true)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create ops admin: %d %s", resp.StatusCode, body)
	}
	ops := env.client()
	ops.login("ops", "ops-password-123")
	resp, body = ops.do("PUT", "/api/v1/admin/settings", map[string]any{"deletion_enabled": true}, true)
	if resp.StatusCode != http.StatusForbidden || errorReasonT(t, body) != "root_admin_required" {
		t.Fatalf("non-root settings change: %d %s", resp.StatusCode, body)
	}

	resp, body = admin.do("PUT", "/api/v1/admin/settings", map[string]any{"deletion_enabled": true}, true)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("root enable deletion: %d %s", resp.StatusCode, body)
	}

	// A usable account sees the switch on and no pending deletion.
	resp, body = user.do("GET", "/api/v1/auth/deletion", nil, false)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status after enable: %d %s", resp.StatusCode, body)
	}
	if status = decodeJSONT[deletionStatusT](t, body); !status.Enabled || status.DeletedAt != nil {
		t.Fatalf("unexpected status after enable: %+v", status)
	}

	// Wrong password does not start a deletion.
	resp, body = user.do("POST", "/api/v1/auth/deletion/request", map[string]any{"password": "wrong-password"}, true)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("request with wrong password: %d %s", resp.StatusCode, body)
	}

	resp, body = user.do("POST", "/api/v1/auth/deletion/request", map[string]any{"password": "m52c-password-1"}, true)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("request: %d %s", resp.StatusCode, body)
	}
	ticket := decodeJSONT[deletionTicketT](t, body)
	if len(ticket.CancelToken) < 32 {
		t.Fatalf("short cancel token: %q", ticket.CancelToken)
	}
	purgeAt, err := time.Parse(time.RFC3339, ticket.PurgeAt)
	if err != nil {
		t.Fatalf("purge_at %q is not RFC3339: %v", ticket.PurgeAt, err)
	}
	if d := time.Until(purgeAt); d < 71*time.Hour || d > 73*time.Hour {
		t.Fatalf("purge_at %v is not ~72h out", d)
	}

	// The request revoked the session and the account cannot log back in.
	if resp, _ := user.do("GET", "/api/v1/auth/me", nil, false); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("session after request: want 401, got %d", resp.StatusCode)
	}
	relogin := env.client()
	relogin.do("GET", "/health", nil, false)
	resp, body = relogin.do("POST", "/api/v1/auth/login",
		map[string]string{"username": "m52c", "password": "m52c-password-1"}, true)
	if resp.StatusCode != http.StatusForbidden || errorReasonT(t, body) != "deletion_pending" {
		t.Fatalf("login while deleting: %d %s", resp.StatusCode, body)
	}
	// The admin list exposes both timestamps.
	resp, body = admin.do("GET", "/api/v1/admin/users?q=m52c", nil, false)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("admin list: %d %s", resp.StatusCode, body)
	}
	var page struct {
		Items []struct {
			DeletedAt  *string `json:"deleted_at"`
			PurgeAfter *string `json:"purge_after"`
		} `json:"items"`
	}
	page = decodeJSONT[struct {
		Items []struct {
			DeletedAt  *string `json:"deleted_at"`
			PurgeAfter *string `json:"purge_after"`
		} `json:"items"`
	}](t, body)
	if len(page.Items) != 1 || page.Items[0].DeletedAt == nil || page.Items[0].PurgeAfter == nil {
		t.Fatalf("admin list missing deletion timestamps: %+v", page)
	}

	// Cancel is anonymous but still needs the CSRF double submit.
	anon := env.client()
	anon.do("GET", "/health", nil, false)
	if resp, _ := anon.do("POST", "/api/v1/auth/deletion/cancel", map[string]any{"token": ticket.CancelToken}, false); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("cancel without csrf: want 403, got %d", resp.StatusCode)
	}
	resp, body = anon.do("POST", "/api/v1/auth/deletion/cancel", map[string]any{"token": "not-a-real-token"}, true)
	if resp.StatusCode != http.StatusBadRequest || errorReasonT(t, body) != "invalid_value" {
		t.Fatalf("cancel with bad token: %d %s", resp.StatusCode, body)
	}
	resp, body = anon.do("POST", "/api/v1/auth/deletion/cancel", map[string]any{"token": ticket.CancelToken}, true)
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), `"restored":true`) {
		t.Fatalf("cancel: %d %s", resp.StatusCode, body)
	}
	// One-time: replaying the token fails.
	if resp, _ := anon.do("POST", "/api/v1/auth/deletion/cancel", map[string]any{"token": ticket.CancelToken}, true); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("replayed cancel: want 400, got %d", resp.StatusCode)
	}

	// Restored account can log in again.
	again := env.client()
	again.login("m52c", "m52c-password-1")

	// Administrator restore lifts a new grace window and burns its token.
	resp, body = again.do("POST", "/api/v1/auth/deletion/request", map[string]any{"password": "m52c-password-1"}, true)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("second request: %d %s", resp.StatusCode, body)
	}
	second := decodeJSONT[deletionTicketT](t, body)
	resp, body = admin.do("POST", "/api/v1/admin/users/"+itoa(created.User.ID)+"/restore", nil, true)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("admin restore: %d %s", resp.StatusCode, body)
	}
	restored := decodeJSONT[createdUserT](t, body)
	if restored.User.Status != "active" {
		t.Fatalf("restore did not reactivate: %+v", restored.User)
	}
	// Restoring is idempotent only in the sense that a non-deleting account is
	// refused: the same call again reports invalid_state.
	if resp, body := admin.do("POST", "/api/v1/admin/users/"+itoa(created.User.ID)+"/restore", nil, true); resp.StatusCode != http.StatusConflict {
		t.Fatalf("restore of active account: want 409, got %d %s", resp.StatusCode, body)
	}
	if resp, _ := anon.do("POST", "/api/v1/auth/deletion/cancel", map[string]any{"token": second.CancelToken}, true); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("restore did not burn the token: got %d", resp.StatusCode)
	}
	relogin2 := env.client()
	relogin2.login("m52c", "m52c-password-1")

	// Guard: an admin cannot purge themselves.
	resp, body = admin.do("POST", "/api/v1/admin/users/1/purge", nil, true)
	if resp.StatusCode != http.StatusConflict || errorReasonT(t, body) != "self_target" {
		t.Fatalf("self purge: %d %s", resp.StatusCode, body)
	}

	// Purging while active is allowed for an admin (immediate delete) but the
	// tombstone reserves the username afterwards.
	resp, body = relogin2.do("POST", "/api/v1/auth/deletion/request", map[string]any{"password": "m52c-password-1"}, true)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("third request: %d %s", resp.StatusCode, body)
	}
	resp, body = admin.do("POST", "/api/v1/admin/users/"+itoa(created.User.ID)+"/purge", nil, true)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("admin purge: %d %s", resp.StatusCode, body)
	}
	// The stored cookies were cleared when the deletion was requested, so a
	// returning browser picks up a fresh CSRF token before logging in again.
	fresh := env.client()
	fresh.do("GET", "/health", nil, false)
	if resp, body := fresh.do("POST", "/api/v1/auth/login",
		map[string]string{"username": "m52c", "password": "m52c-password-1"}, true); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("purged account still logs in: %d %s", resp.StatusCode, body)
	}
	resp, body = admin.do("POST", "/api/v1/admin/users", map[string]any{
		"username": "m52c", "password": "m52c-password-1", "role": "user",
	}, true)
	if resp.StatusCode != http.StatusConflict || errorReasonT(t, body) != "username_reserved" {
		t.Fatalf("recreate purged username: %d %s", resp.StatusCode, body)
	}
}

// TestDeletionEndpointMethodAndStatus guards the route table entry: a wrong
// method on the status endpoint is a 405 with Allow, not an unknown-endpoint 404.
func TestDeletionEndpointMethodAndStatus(t *testing.T) {
	env := newTestEnv(t)
	c := env.client()
	c.login("admin", env.password)
	resp, _ := c.do("POST", "/api/v1/auth/deletion", nil, true)
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("POST /auth/deletion: want 405, got %d", resp.StatusCode)
	}
	if allow := resp.Header.Get("Allow"); allow != "GET" {
		t.Fatalf("Allow = %q", allow)
	}
}
