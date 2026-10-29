package httpx

import (
	"net/http"
	"strings"
	"testing"
)

// ---- helpers ---------------------------------------------------------------

// bootstrapCSRF installs the double-submit CSRF cookie a mutating request needs;
// the frontend does the same via GET /health before registering.
func bootstrapCSRF(c *apiClient) {
	c.do(http.MethodGet, "/health", nil, false)
}

// register posts a registration payload.
func (c *apiClient) register(body map[string]any) (*http.Response, []byte) {
	bootstrapCSRF(c)
	return c.do(http.MethodPost, "/api/v1/auth/register", body, true)
}

// enableRegistration turns self registration on through the admin API as the
// bootstrap (root) administrator.
func enableRegistration(t *testing.T, env *testEnv, c *apiClient) {
	t.Helper()
	c.login("admin", env.password)
	resp, body := c.do(http.MethodPut, "/api/v1/admin/settings",
		map[string]any{"registration_enabled": true}, true)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("enable registration = %d %s", resp.StatusCode, body)
	}
}

type registrationStatusT struct {
	Enabled bool `json:"enabled"`
}

type registerResponseT struct {
	User struct {
		ID       int64  `json:"id"`
		Username string `json:"username"`
		Role     string `json:"role"`
		Email    string `json:"email"`
		Phone    string `json:"phone"`
		IsRoot   bool   `json:"is_root"`
	} `json:"user"`
}

type meIdentityT struct {
	User struct {
		ID       int64  `json:"id"`
		Username string `json:"username"`
		Email    string `json:"email"`
		Phone    string `json:"phone"`
		IsRoot   bool   `json:"is_root"`
	} `json:"user"`
}

type adminCreatedUserT struct {
	User struct {
		ID     int64  `json:"id"`
		Email  string `json:"email"`
		Phone  string `json:"phone"`
		IsRoot bool   `json:"is_root"`
	} `json:"user"`
	InitialPassword string `json:"initial_password"`
}

// ---- registration ------------------------------------------------------------

func TestRegistrationStatus(t *testing.T) {
	env := newTestEnv(t)
	c := env.client()

	resp, body := c.do(http.MethodGet, "/api/v1/auth/registration", nil, false)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("registration status = %d %s", resp.StatusCode, body)
	}
	if got := decodeJSONT[registrationStatusT](t, body); got.Enabled {
		t.Fatal("registration reported open by default")
	}

	enableRegistration(t, env, c)
	resp, body = c.do(http.MethodGet, "/api/v1/auth/registration", nil, false)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("registration status = %d %s", resp.StatusCode, body)
	}
	if got := decodeJSONT[registrationStatusT](t, body); !got.Enabled {
		t.Fatalf("registration still closed after enabling: %s", body)
	}
}

func TestRegisterDisabled(t *testing.T) {
	env := newTestEnv(t)
	c := env.client()

	resp, body := c.register(map[string]any{
		"username": "carol", "password": "carol-password-1",
	})
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("register = %d %s, want 403", resp.StatusCode, body)
	}
	apiErr := decodeAPIError(t, body)
	if apiErr.Error.Code != "forbidden" || apiErr.Error.Reason != "registration_disabled" {
		t.Fatalf("error = %+v, want forbidden/registration_disabled", apiErr.Error)
	}
}

func TestRegisterAndLogin(t *testing.T) {
	env := newTestEnv(t)
	c := env.client()
	enableRegistration(t, env, c)

	resp, body := c.register(map[string]any{
		"username": "carol",
		"email":    "Carol@Example.com",
		"phone":    "13800138000",
		"password": "carol-password-1",
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("register = %d %s, want 201", resp.StatusCode, body)
	}
	for _, ck := range resp.Cookies() {
		if ck.Name == sessionCookieName || ck.Name == csrfCookieName {
			t.Fatalf("registration must not create a session cookie, got %s", ck.Name)
		}
	}
	created := decodeJSONT[registerResponseT](t, body)
	if created.User.Username != "carol" || created.User.Role != "user" || created.User.IsRoot {
		t.Fatalf("unexpected user: %s", body)
	}
	if created.User.Email != "Carol@Example.com" || created.User.Phone != "13800138000" {
		t.Fatalf("identifiers not echoed: %s", body)
	}
	if strings.Contains(string(body), "password") {
		t.Fatalf("registration response leaks a password field: %s", body)
	}

	// Every identifier logs the new account in.
	for _, identifier := range []string{"carol", "CAROL@example.com", "13800138000"} {
		fresh := env.client()
		fresh.login(identifier, "carol-password-1")
		_, meBody := fresh.do(http.MethodGet, "/api/v1/auth/me", nil, false)
		me := decodeJSONT[meIdentityT](t, meBody)
		if me.User.ID != created.User.ID {
			t.Fatalf("login %q returned user %d, want %d", identifier, me.User.ID, created.User.ID)
		}
		if me.User.Email != "Carol@Example.com" || me.User.Phone != "13800138000" || me.User.IsRoot {
			t.Fatalf("me identity = %s", meBody)
		}
	}
}

func TestRegisterDuplicateAndValidation(t *testing.T) {
	env := newTestEnv(t)
	c := env.client()
	enableRegistration(t, env, c)

	if resp, body := c.register(map[string]any{
		"username": "dana", "email": "dana@example.com", "password": "dana-password-1",
	}); resp.StatusCode != http.StatusCreated {
		t.Fatalf("first register = %d %s", resp.StatusCode, body)
	}

	resp, body := c.register(map[string]any{
		"username": "dana", "password": "other-password-1",
	})
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("duplicate register = %d %s, want 409", resp.StatusCode, body)
	}
	if e := decodeAPIError(t, body); e.Error.Code != "name_conflict" || e.Error.Reason != "identifier_taken" {
		t.Fatalf("duplicate error = %+v", e.Error)
	}

	resp, body = c.register(map[string]any{
		"username": "erin", "password": "short",
	})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("short password = %d %s, want 400", resp.StatusCode, body)
	}
	if e := decodeAPIError(t, body); e.Error.Reason != "password_too_short" {
		t.Fatalf("short password error = %+v", e.Error)
	}

	resp, body = c.register(map[string]any{"password": "valid-password-1"})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("missing identifiers = %d %s, want 400", resp.StatusCode, body)
	}
}

func TestRegisterRateLimited(t *testing.T) {
	env := newTestEnv(t)
	c := env.client()
	enableRegistration(t, env, c)

	usernames := []string{"ra", "rb", "rc", "rd", "re"}
	for i, name := range usernames {
		resp, body := c.register(map[string]any{
			"username": name, "password": "limit-password-1",
		})
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("register %d = %d %s", i, resp.StatusCode, body)
		}
	}
	resp, body := c.register(map[string]any{
		"username": "rf", "password": "limit-password-1",
	})
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("sixth register = %d %s, want 429", resp.StatusCode, body)
	}
	e := decodeAPIError(t, body)
	if e.Error.Code != "too_many_attempts" || e.Error.Reason != "rate_limited" {
		t.Fatalf("rate limit error = %+v", e.Error)
	}
	if got := resp.Header.Get("Retry-After"); got == "" {
		t.Error("rate limited response has no Retry-After header")
	}
}

// ---- admin surface ----------------------------------------------------------

func TestAdminUserIdentifiersMasked(t *testing.T) {
	env := newTestEnv(t)
	c := env.client()
	c.login("admin", env.password)

	resp, body := c.do(http.MethodPost, "/api/v1/admin/users", map[string]any{
		"username": "masked",
		"email":    "alice@example.com",
		"phone":    "13800138000",
		"password": "masked-password-1",
	}, true)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("admin create = %d %s", resp.StatusCode, body)
	}
	created := decodeJSONT[adminCreatedUserT](t, body)
	if created.User.Email != "a***@example.com" {
		t.Fatalf("created email = %q, want masked", created.User.Email)
	}
	if created.User.Phone != "138****8000" {
		t.Fatalf("created phone = %q, want masked", created.User.Phone)
	}

	resp, body = c.do(http.MethodGet, "/api/v1/admin/users?q=alice@example.com", nil, false)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("admin list = %d %s", resp.StatusCode, body)
	}
	page := decodeJSONT[adminUsersT](t, body)
	if page.Total != 1 || len(page.Items) != 1 || page.Items[0].Username != "masked" {
		t.Fatalf("email search = %s", body)
	}

	resp, body = c.do(http.MethodGet, "/api/v1/admin/users", nil, false)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("admin list = %d %s", resp.StatusCode, body)
	}
	page = decodeJSONT[adminUsersT](t, body)
	var adminItem, userItem bool
	for _, item := range page.Items {
		switch item.Username {
		case "admin":
			adminItem = true
			if !item.IsRoot {
				t.Fatalf("bootstrap admin is not root: %s", body)
			}
			if item.Email != "" || item.Phone != "" {
				t.Fatalf("admin identifiers = %q/%q, want empty", item.Email, item.Phone)
			}
		case "masked":
			userItem = true
			if item.IsRoot {
				t.Fatalf("plain user reported root: %s", body)
			}
			if item.Email != "a***@example.com" || item.Phone != "138****8000" {
				t.Fatalf("masked item = %s", body)
			}
		}
	}
	if !adminItem || !userItem {
		t.Fatalf("admin list is missing accounts: %s", body)
	}
}

func TestRootAdminGuards(t *testing.T) {
	env := newTestEnv(t)
	root := env.client()
	root.login("admin", env.password)
	_, meBody := root.do(http.MethodGet, "/api/v1/auth/me", nil, false)
	rootID := decodeJSONT[meIdentityT](t, meBody).User.ID

	// A second administrator is allowed to try demoting root.
	resp, body := root.do(http.MethodPost, "/api/v1/admin/users", map[string]any{
		"username": "boss2", "password": "boss2-password-1", "role": "admin",
	}, true)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create second admin = %d %s", resp.StatusCode, body)
	}
	boss := env.client()
	boss.login("boss2", "boss2-password-1")

	path := "/api/v1/admin/users/" + itoa(rootID)
	for _, payload := range []map[string]any{{"status": "disabled"}, {"role": "user"}} {
		resp, body := boss.do(http.MethodPatch, path, payload, true)
		if resp.StatusCode != http.StatusConflict {
			t.Fatalf("patch root %v = %d %s, want 409", payload, resp.StatusCode, body)
		}
		e := decodeAPIError(t, body)
		if e.Error.Code != "invalid_state" || e.Error.Reason != "root_admin" {
			t.Fatalf("patch root %v error = %+v", payload, e.Error)
		}
	}
	// Root keeps its quota editable.
	resp, body = boss.do(http.MethodPatch, path, map[string]any{"quota_bytes": 2048}, true)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("patch root quota = %d %s", resp.StatusCode, body)
	}
}

func TestNonRootAdminCannotToggleRegistration(t *testing.T) {
	env := newTestEnv(t)
	root := env.client()
	root.login("admin", env.password)
	resp, body := root.do(http.MethodPost, "/api/v1/admin/users", map[string]any{
		"username": "boss3", "password": "boss3-password-1", "role": "admin",
	}, true)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create second admin = %d %s", resp.StatusCode, body)
	}
	boss := env.client()
	boss.login("boss3", "boss3-password-1")

	resp, body = boss.do(http.MethodPut, "/api/v1/admin/settings",
		map[string]any{"registration_enabled": true}, true)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("non-root toggle = %d %s, want 403", resp.StatusCode, body)
	}
	e := decodeAPIError(t, body)
	if e.Error.Code != "forbidden" || e.Error.Reason != "root_admin_required" {
		t.Fatalf("non-root toggle error = %+v", e.Error)
	}
	// Sending the unchanged value is not a change and stays allowed.
	resp, body = boss.do(http.MethodPut, "/api/v1/admin/settings",
		map[string]any{"registration_enabled": false}, true)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("non-root no-op toggle = %d %s", resp.StatusCode, body)
	}
	// Other keys stay editable for every administrator.
	resp, body = boss.do(http.MethodPut, "/api/v1/admin/settings",
		map[string]any{"site_name": "Rapig Team"}, true)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("non-root site name = %d %s", resp.StatusCode, body)
	}
	settings := decodeJSONT[adminSettingsT](t, body)
	if settings.SiteName != "Rapig Team" || settings.RegistrationEnabled {
		t.Fatalf("settings = %+v", settings)
	}
	// A rejected patch must not leave earlier fields applied.
	resp, _ = boss.do(http.MethodPut, "/api/v1/admin/settings",
		map[string]any{"site_name": "Changed", "registration_enabled": true}, true)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("mixed patch = %d, want 403", resp.StatusCode)
	}
	_, body = boss.do(http.MethodGet, "/api/v1/admin/settings", nil, false)
	if got := decodeJSONT[adminSettingsT](t, body); got.SiteName != "Rapig Team" {
		t.Fatalf("rejected patch was partially applied: %+v", got)
	}
}

// TestAdminCreateUserIdentifierConflicts checks the admin create path uses the
// same cross-namespace uniqueness rules as registration.
func TestAdminCreateUserIdentifierConflicts(t *testing.T) {
	env := newTestEnv(t)
	c := env.client()
	c.login("admin", env.password)

	if resp, body := c.do(http.MethodPost, "/api/v1/admin/users", map[string]any{
		"username": "gina", "email": "gina@example.com", "password": "gina-password-1",
	}, true); resp.StatusCode != http.StatusCreated {
		t.Fatalf("create gina = %d %s", resp.StatusCode, body)
	}
	resp, body := c.do(http.MethodPost, "/api/v1/admin/users", map[string]any{
		"username": "hank", "email": "GINA@example.com", "password": "hank-password-1",
	}, true)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("duplicate email = %d %s, want 409", resp.StatusCode, body)
	}
	if e := decodeAPIError(t, body); e.Error.Reason != "identifier_taken" {
		t.Fatalf("duplicate email reason = %+v", e.Error)
	}
	resp, body = c.do(http.MethodPost, "/api/v1/admin/users", map[string]any{
		"username": "gina2", "email": "not-an-email", "password": "gina2-password-1",
	}, true)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("invalid email = %d %s, want 400", resp.StatusCode, body)
	}
}

// itoa is defined in server_test.go and reused here for path ids.
