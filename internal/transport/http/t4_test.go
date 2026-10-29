package httpx

import (
	"bytes"
	"context"
	"net/http"
	"testing"
	"time"

	"rapig/internal/domain/access"
	domainidentity "rapig/internal/domain/identity"
	sqlstore "rapig/internal/infra/sql"
)

// newOtherUser creates a second account (no HTTP API for that in M1) and one
// file it owns, so admin-only paths can be tested against foreign resources.
func newOtherUser(t *testing.T, env *testEnv, username string, content []byte) (int64, int64) {
	t.Helper()
	ctx := context.Background()
	roleID, err := env.db.RoleIDByCode(ctx, "user")
	if err != nil {
		t.Fatalf("role id: %v", err)
	}
	user, err := env.db.CreateUser(ctx, domainidentity.User{
		Username:       username,
		UsernameFolded: username,
		DisplayName:    username,
		PasswordHash:   "not-a-real-hash",
		RoleID:         roleID,
	})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	sc := access.UserScope(user.ID, false)
	root, err := env.db.EnsureRoot(ctx, user.ID, env.policyID)
	if err != nil {
		t.Fatalf("ensure root: %v", err)
	}
	sha, size, err := env.store.Put(ctx, bytes.NewReader(content))
	if err != nil {
		t.Fatalf("store put: %v", err)
	}
	blob, _, err := env.db.InsertBlob(ctx, sha, size)
	if err != nil {
		t.Fatalf("insert blob: %v", err)
	}
	node, err := env.db.CommitCompletedUpload(ctx, sc, sqlstore.CompletedUploadInput{
		ParentID:  root.ID,
		Name:      "secret.txt",
		Path:      "secret.txt",
		Mime:      "text/plain",
		SHA256:    sha,
		Size:      size,
		BlobID:    blob.ID,
		CreatedAt: time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("commit upload: %v", err)
	}
	return user.ID, node.ID
}

func TestPasswordChangeFlow(t *testing.T) {
	env := newTestEnv(t)
	c := env.client()
	c.login("admin", env.password)

	// wrong current password
	resp, body := c.do(http.MethodPost, "/api/v1/auth/password",
		map[string]string{"current_password": "nope", "new_password": "newpass123"}, true)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("wrong current: %d %s", resp.StatusCode, body)
	}
	if got := decodeAPIError(t, body).Error.Code; got != "invalid_credentials" {
		t.Fatalf("wrong current code = %q", got)
	}

	// too short
	resp, body = c.do(http.MethodPost, "/api/v1/auth/password",
		map[string]string{"current_password": env.password, "new_password": "short"}, true)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("short: %d %s", resp.StatusCode, body)
	}
	if got := decodeAPIError(t, body).Error.Reason; got != "password_too_short" {
		t.Fatalf("short reason = %q", got)
	}

	// equal to the current password
	resp, body = c.do(http.MethodPost, "/api/v1/auth/password",
		map[string]string{"current_password": env.password, "new_password": env.password}, true)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("reuse: %d %s", resp.StatusCode, body)
	}
	if got := decodeAPIError(t, body).Error.Reason; got != "password_reuse" {
		t.Fatalf("reuse reason = %q", got)
	}

	// success
	resp, body = c.do(http.MethodPost, "/api/v1/auth/password",
		map[string]string{"current_password": env.password, "new_password": "newpass123"}, true)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("change: %d %s", resp.StatusCode, body)
	}

	// the current session still works
	resp, _ = c.do(http.MethodGet, "/api/v1/auth/me", nil, false)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("me after change: %d", resp.StatusCode)
	}

	// old password no longer logs in, new one does
	old := env.client()
	old.do(http.MethodGet, "/health", nil, false)
	resp, _ = old.do(http.MethodPost, "/api/v1/auth/login",
		map[string]string{"username": "admin", "password": env.password}, true)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("old password: %d", resp.StatusCode)
	}
	fresh := env.client()
	fresh.login("admin", "newpass123")
}

func TestLoginLockout(t *testing.T) {
	env := newTestEnv(t)
	c := env.client()
	c.do(http.MethodGet, "/health", nil, false)

	for i := 1; i <= 4; i++ {
		resp, body := c.do(http.MethodPost, "/api/v1/auth/login",
			map[string]string{"username": "admin", "password": "wrong"}, true)
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("attempt %d: %d %s", i, resp.StatusCode, body)
		}
	}

	// the 5th failure locks the account for 30s
	resp, body := c.do(http.MethodPost, "/api/v1/auth/login",
		map[string]string{"username": "admin", "password": "wrong"}, true)
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("5th attempt: %d %s", resp.StatusCode, body)
	}
	if got := resp.Header.Get("Retry-After"); got == "" {
		t.Fatal("5th attempt: missing Retry-After")
	}
	if got := decodeAPIError(t, body).Error.Reason; got != "locked" {
		t.Fatalf("5th attempt reason = %q", got)
	}

	// even the correct password is refused while locked
	resp, body = c.do(http.MethodPost, "/api/v1/auth/login",
		map[string]string{"username": "admin", "password": env.password}, true)
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("locked attempt with correct password: %d %s", resp.StatusCode, body)
	}
}
