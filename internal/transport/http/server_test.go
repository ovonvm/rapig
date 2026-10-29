package httpx

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	domainaccess "rapig/internal/domain/access"
	domainfiles "rapig/internal/domain/files"
	domainidentity "rapig/internal/domain/identity"
	"rapig/internal/infra/sql"
	storagereg "rapig/internal/infra/storage"
	"rapig/internal/platform/config"

	accountsvc "rapig/internal/app/account"
	adminsvc "rapig/internal/app/admin"
	filessvc "rapig/internal/app/files"
	groupssvc "rapig/internal/app/groups"
	idsvc "rapig/internal/app/identity"
	sharesvc "rapig/internal/app/shares"
	transfersvc "rapig/internal/app/transfers"
	upsvc "rapig/internal/app/uploads"
	"rapig/internal/infra/fetch"
	storagelocal "rapig/internal/infra/storage/local"
	"rapig/migrations"
)

type testEnv struct {
	t        *testing.T
	server   *httptest.Server
	db       *sqlstore.DB
	ids      *idsvc.Service
	store    *storagelocal.Store
	admin    *adminsvc.Service
	policyID int64
	password string
}

func newTestEnv(t *testing.T) *testEnv {
	t.Helper()
	return newTestEnvWith(t)
}

// newTestEnvWith builds the same test server with extra transfer options (for
// example an enabled qBittorrent client).
func newTestEnvWith(t *testing.T, opts ...transfersvc.Options) *testEnv {
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
	policyID, err := db.EnsureDefaultStoragePolicy(ctx, filepath.Join(dir, "storage"))
	if err != nil {
		t.Fatalf("storage policy: %v", err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	ids := idsvc.NewService(db, log)
	boot, err := ids.Bootstrap(ctx, filepath.Join(dir, "bootstrap"), policyID)
	if err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	if !boot.Created {
		t.Fatal("bootstrap did not create the admin account")
	}

	webFS := fstest.MapFS{
		"index.html":    {Data: []byte("<!doctype html><html><body><div id=\"app\"></div></body></html>")},
		"assets/app.js": {Data: []byte("console.log('rapig')")},
	}
	store, err := storagelocal.New(filepath.Join(dir, "storage"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	registry := storagereg.NewRegistry(db, log)
	fls := filessvc.NewService(db, registry, log)
	ups := upsvc.New(db, registry, fls, log, 2, 64<<10)
	shs := sharesvc.New(db, fls, log)
	trs := transfersvc.New(db, registry, fetch.New(true), log, 2, opts...)
	adm := adminsvc.New(db, log)
	acc := accountsvc.New(db, ids, log)
	grp := groupssvc.NewService(db, log)
	cfg := config.Config{Addr: "127.0.0.1:0", DataDir: dir, LogLevel: "error"}
	handler := New(cfg, log, db, registry, ids, fls, ups, shs, trs, adm, acc, grp, webFS).Handler()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return &testEnv{t: t, server: srv, db: db, ids: ids, store: store, admin: adm, policyID: policyID, password: boot.Password}
}

// ---- HTTP client helpers ---------------------------------------------------

type apiClient struct {
	t      *testing.T
	base   string
	origin string
	http   *http.Client
}

func (e *testEnv) client() *apiClient {
	jar, err := cookiejar.New(nil)
	if err != nil {
		e.t.Fatalf("cookie jar: %v", err)
	}
	return &apiClient{
		t:      e.t,
		base:   e.server.URL,
		origin: e.server.URL,
		http:   &http.Client{Jar: jar},
	}
}

func (c *apiClient) csrfToken() string {
	u, err := url.Parse(c.base)
	if err != nil {
		return ""
	}
	for _, ck := range c.http.Jar.Cookies(u) {
		if ck.Name == csrfCookieName {
			return ck.Value
		}
	}
	return ""
}

func (c *apiClient) do(method, path string, body any, unsafe bool) (*http.Response, []byte) {
	return c.doWithOrigin(method, path, body, unsafe, c.origin)
}

func (c *apiClient) doWithOrigin(method, path string, body any, unsafe bool, origin string) (*http.Response, []byte) {
	c.t.Helper()
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			c.t.Fatalf("marshal body: %v", err)
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, c.base+path, reader)
	if err != nil {
		c.t.Fatalf("new request: %v", err)
	}
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if unsafe {
		if tok := c.csrfToken(); tok != "" {
			req.Header.Set("X-CSRF-Token", tok)
		}
	}
	resp, err := c.http.Do(req)
	if err != nil {
		c.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		c.t.Fatalf("read body: %v", err)
	}
	return resp, data
}

// doRaw sends a body verbatim (part uploads and range requests) and returns
// the raw response. It is the only helper that can set arbitrary headers.
func (c *apiClient) doRaw(method, path string, body []byte, contentType string, unsafe bool, headers map[string]string) (*http.Response, []byte) {
	c.t.Helper()
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequest(method, c.base+path, reader)
	if err != nil {
		c.t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Origin", c.origin)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	if unsafe {
		if tok := c.csrfToken(); tok != "" {
			req.Header.Set("X-CSRF-Token", tok)
		}
	}
	resp, err := c.http.Do(req)
	if err != nil {
		c.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		c.t.Fatalf("read body: %v", err)
	}
	return resp, data
}

func (c *apiClient) login(username, password string) {
	c.t.Helper()
	// Any safe request installs the CSRF cookie the double submit scheme needs.
	c.do(http.MethodGet, "/health", nil, false)
	resp, body := c.do(http.MethodPost, "/api/v1/auth/login",
		map[string]string{"username": username, "password": password}, true)
	if resp.StatusCode != http.StatusOK {
		c.t.Fatalf("login failed: %d %s", resp.StatusCode, body)
	}
}

// ---- helpers ---------------------------------------------------------------

type apiError struct {
	Error struct {
		Code      string `json:"code"`
		Message   string `json:"message"`
		RequestID string `json:"request_id"`
		Reason    string `json:"reason"`
	} `json:"error"`
}

func decodeAPIError(t *testing.T, body []byte) apiError {
	t.Helper()
	var out apiError
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("decode error body %q: %v", body, err)
	}
	return out
}

type meResponse struct {
	User struct {
		ID       int64  `json:"id"`
		Username string `json:"username"`
		Role     string `json:"role"`
	} `json:"user"`
}

type listNodesResponseT struct {
	Items []struct {
		ID       int64  `json:"id"`
		ParentID int64  `json:"parent_id"`
		Kind     string `json:"kind"`
		Name     string `json:"name"`
	} `json:"items"`
	Total  int `json:"total"`
	Limit  int `json:"limit"`
	Offset int `json:"offset"`
}

func decodeJSONT[T any](t *testing.T, body []byte) T {
	t.Helper()
	var out T
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("decode %q: %v", body, err)
	}
	return out
}

// ---- tests -----------------------------------------------------------------

func TestHealth(t *testing.T) {
	env := newTestEnv(t)
	c := env.client()
	resp, body := c.do(http.MethodGet, "/health", nil, false)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("health = %d %s", resp.StatusCode, body)
	}
	if got := resp.Header.Get("X-Request-ID"); got == "" {
		t.Error("health response is missing X-Request-ID")
	}
	if !strings.Contains(string(body), `"status":"ok"`) || !strings.Contains(string(body), `"db":"ok"`) {
		t.Errorf("unexpected health body: %s", body)
	}
}

func TestProtectedEndpointRequiresLogin(t *testing.T) {
	env := newTestEnv(t)
	c := env.client()
	resp, body := c.do(http.MethodGet, "/api/v1/nodes", nil, false)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("nodes without login = %d %s", resp.StatusCode, body)
	}
	if got := decodeAPIError(t, body).Error.Code; got != "unauthenticated" {
		t.Errorf("error code = %q, want unauthenticated", got)
	}
}

func TestLoginRejectsBadCredentialsWithoutLeaking(t *testing.T) {
	env := newTestEnv(t)
	c := env.client()
	c.do(http.MethodGet, "/health", nil, false)
	resp, body := c.do(http.MethodPost, "/api/v1/auth/login",
		map[string]string{"username": "admin", "password": "wrong"}, true)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("wrong password = %d %s", resp.StatusCode, body)
	}
	if got := decodeAPIError(t, body).Error.Code; got != "invalid_credentials" {
		t.Errorf("error code = %q, want invalid_credentials", got)
	}

	// Unknown users must produce the same status and code as wrong passwords.
	resp, body = c.do(http.MethodPost, "/api/v1/auth/login",
		map[string]string{"username": "ghost", "password": "wrong"}, true)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unknown user = %d %s", resp.StatusCode, body)
	}
	if got := decodeAPIError(t, body).Error.Code; got != "invalid_credentials" {
		t.Errorf("error code = %q, want invalid_credentials", got)
	}
}

func TestMutatingAPINeedsCSRF(t *testing.T) {
	env := newTestEnv(t)
	c := env.client()
	c.do(http.MethodGet, "/health", nil, false)
	// Same body and origin as the real login, but no X-CSRF-Token header.
	raw, _ := json.Marshal(map[string]string{"username": "admin", "password": env.password})
	req, err := http.NewRequest(http.MethodPost, env.server.URL+"/api/v1/auth/login", bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Origin", env.server.URL)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("login without csrf = %d %s", resp.StatusCode, body)
	}
	if got := decodeAPIError(t, body).Error.Code; got != "csrf_failed" {
		t.Errorf("error code = %q, want csrf_failed", got)
	}
}

func TestLoginFlowCreateListRefreshLogout(t *testing.T) {
	env := newTestEnv(t)
	c := env.client()
	c.login("admin", env.password)

	resp, body := c.do(http.MethodGet, "/api/v1/auth/me", nil, false)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("/me = %d %s", resp.StatusCode, body)
	}
	me := decodeJSONT[meResponse](t, body)
	if me.User.Username != "admin" || me.User.Role != "admin" {
		t.Errorf("unexpected /me: %+v", me.User)
	}

	resp, body = c.do(http.MethodGet, "/api/v1/nodes", nil, false)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list root = %d %s", resp.StatusCode, body)
	}
	list := decodeJSONT[listNodesResponseT](t, body)
	if list.Total != 0 {
		t.Fatalf("fresh root should be empty, got total %d", list.Total)
	}
	if !strings.Contains(string(body), `"items":[]`) {
		t.Errorf("empty item list must serialize as [], body=%s", body)
	}

	resp, body = c.do(http.MethodPost, "/api/v1/nodes",
		map[string]any{"parent": 0, "name": "测试", "kind": "dir"}, true)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create dir = %d %s", resp.StatusCode, body)
	}
	created := decodeJSONT[struct {
		ID       int64  `json:"id"`
		ParentID int64  `json:"parent_id"`
		Kind     string `json:"kind"`
		Name     string `json:"name"`
	}](t, body)
	if created.Name != "测试" || created.Kind != "dir" || created.ParentID != 0 || created.ID == 0 {
		t.Fatalf("unexpected created node: %+v", created)
	}

	// Refresh: the directory must still be there and come from the database.
	resp, body = c.do(http.MethodGet, "/api/v1/nodes", nil, false)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list after create = %d %s", resp.StatusCode, body)
	}
	list = decodeJSONT[listNodesResponseT](t, body)
	if list.Total != 1 || len(list.Items) != 1 || list.Items[0].Name != "测试" {
		t.Fatalf("list after create = %+v", list)
	}

	// Siblings are unique case-insensitively.
	resp, body = c.do(http.MethodPost, "/api/v1/nodes",
		map[string]any{"parent": 0, "name": "测试", "kind": "dir"}, true)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("duplicate = %d %s", resp.StatusCode, body)
	}
	if got := decodeAPIError(t, body).Error.Code; got != "name_conflict" {
		t.Errorf("duplicate error code = %q", got)
	}

	cases := []struct {
		name     string
		payload  map[string]any
		status   int
		wantCode string
	}{
		{"invalid name", map[string]any{"parent": 0, "name": "a/b", "kind": "dir"}, 400, "invalid_name"},
		{"empty name", map[string]any{"parent": 0, "name": "", "kind": "dir"}, 400, "invalid_name"},
		{"file kind", map[string]any{"parent": 0, "name": "x", "kind": "file"}, 400, "bad_request"},
		{"missing parent", map[string]any{"parent": 999999, "name": "x", "kind": "dir"}, 404, "parent_not_found"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp, body := c.do(http.MethodPost, "/api/v1/nodes", tc.payload, true)
			if resp.StatusCode != tc.status {
				t.Fatalf("%s = %d %s", tc.name, resp.StatusCode, body)
			}
			if got := decodeAPIError(t, body).Error.Code; got != tc.wantCode {
				t.Errorf("%s error code = %q, want %q", tc.name, got, tc.wantCode)
			}
		})
	}

	if resp, body := c.do(http.MethodGet, "/api/v1/nodes?parent=abc", nil, false); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("parent=abc = %d %s", resp.StatusCode, body)
	}
	if resp, body := c.do(http.MethodGet, "/api/v1/nodes?limit=1000", nil, false); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("limit=1000 = %d %s", resp.StatusCode, body)
	}

	// Logout revokes the session.
	if resp, body := c.do(http.MethodPost, "/api/v1/auth/logout", nil, true); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("logout = %d %s", resp.StatusCode, body)
	}
	if resp, body := c.do(http.MethodGet, "/api/v1/nodes", nil, false); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("after logout = %d %s", resp.StatusCode, body)
	}
}

func TestCSRFOriginMismatchRejected(t *testing.T) {
	env := newTestEnv(t)
	c := env.client()
	c.login("admin", env.password)
	resp, body := c.doWithOrigin(http.MethodPost, "/api/v1/nodes",
		map[string]any{"parent": 0, "name": "evil"}, true, "http://evil.example")
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("cross origin = %d %s", resp.StatusCode, body)
	}
	if got := decodeAPIError(t, body).Error.Reason; got != "origin_mismatch" {
		t.Errorf("reason = %q, want origin_mismatch", got)
	}
}

func TestOtherUsersNodesAreInvisible(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	roleID, err := env.db.RoleIDByCode(ctx, "user")
	if err != nil {
		t.Fatalf("role: %v", err)
	}
	bob, err := env.db.CreateUser(ctx, domainidentity.User{
		Username:       "bob",
		UsernameFolded: "bob",
		DisplayName:    "Bob",
		PasswordHash:   "unusable",
		RoleID:         roleID,
		Status:         domainidentity.StatusActive,
	})
	if err != nil {
		t.Fatalf("create bob: %v", err)
	}
	bobRoot, err := env.db.EnsureRoot(ctx, bob.ID, env.policyID)
	if err != nil {
		t.Fatalf("bob root: %v", err)
	}
	bobDir, err := env.db.CreateDir(ctx, domainaccess.UserScope(bob.ID, false), bobRoot.ID, "private", "private")
	if err != nil {
		t.Fatalf("bob dir: %v", err)
	}
	if bobDir.Kind != domainfiles.KindDir {
		t.Fatalf("bob dir kind = %q", bobDir.Kind)
	}

	c := env.client()
	c.login("admin", env.password)
	for _, ref := range []int64{bobRoot.ID, bobDir.ID} {
		resp, body := c.do(http.MethodGet, "/api/v1/nodes?parent="+itoa(ref), nil, false)
		if resp.StatusCode != http.StatusNotFound {
			t.Fatalf("admin listing bob parent %d = %d %s", ref, resp.StatusCode, body)
		}
		if got := decodeAPIError(t, body).Error.Code; got != "parent_not_found" {
			t.Errorf("foreign parent error code = %q, want parent_not_found", got)
		}
	}
}

func TestSPAAndFallback(t *testing.T) {
	env := newTestEnv(t)
	c := env.client()

	resp, body := c.do(http.MethodGet, "/files", nil, false)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /files = %d %s", resp.StatusCode, body)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("page content type = %q", ct)
	}
	if !strings.Contains(string(body), `id="app"`) {
		t.Errorf("page body is not index.html: %s", body)
	}

	// Known client routes keep 200; unknown page paths still get the shell but
	// with a 404 status so typo'd URLs are not reported as healthy pages.
	for _, route := range []string{"/login", "/register", "/s/demo-token", "/files/42", "/trash", "/settings", "/shares", "/transfers", "/downloads", "/search", "/admin/users", "/admin/audit", "/admin/settings", "/admin/storage", "/admin/groups", "/cancel-deletion"} {
		resp, body = c.do(http.MethodGet, route, nil, false)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("GET %s = %d %s", route, resp.StatusCode, body)
		}
		if !strings.Contains(string(body), `id="app"`) {
			t.Errorf("GET %s body is not index.html", route)
		}
	}
	resp, body = c.do(http.MethodGet, "/fiels", nil, false)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown page = %d %s, want 404", resp.StatusCode, body)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("unknown page content type = %q", ct)
	}
	if !strings.Contains(string(body), `id="app"`) {
		t.Errorf("unknown page must still render the shell")
	}

	resp, body = c.do(http.MethodGet, "/assets/app.js", nil, false)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET asset = %d %s", resp.StatusCode, body)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/javascript") {
		t.Errorf("asset content type = %q", ct)
	}
	if got := resp.Header.Get("Cache-Control"); !strings.Contains(got, "immutable") {
		t.Errorf("asset cache control = %q", got)
	}
	if got := resp.Header.Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("nosniff missing, got %q", got)
	}

	resp, body = c.do(http.MethodGet, "/assets/missing.js", nil, false)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("missing asset = %d %s", resp.StatusCode, body)
	}
	if !strings.Contains(string(body), `"code":"not_found"`) {
		t.Errorf("missing asset must be JSON 404, body=%s", body)
	}

	resp, body = c.do(http.MethodGet, "/api/v1/does-not-exist", nil, false)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown api = %d %s", resp.StatusCode, body)
	}
	apiErr := decodeAPIError(t, body)
	if apiErr.Error.Code != "not_found" || apiErr.Error.Reason != "unknown_endpoint" {
		t.Errorf("unknown api error = %+v", apiErr.Error)
	}

	resp, body = c.do(http.MethodGet, "/api/v1/auth/login", nil, false)
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("GET /api/v1/auth/login = %d %s", resp.StatusCode, body)
	}
	if got := resp.Header.Get("Allow"); got != "POST" {
		t.Errorf("405 Allow = %q, want POST", got)
	}
	if code := decodeAPIError(t, body).Error.Code; code != "method_not_allowed" {
		t.Errorf("wrong method error code = %q", code)
	}

	resp, body = c.do(http.MethodDelete, "/api/v1/nodes", nil, true)
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("DELETE /api/v1/nodes = %d %s", resp.StatusCode, body)
	}
	if got := resp.Header.Get("Allow"); got != "GET, POST" {
		t.Errorf("405 Allow = %q, want \"GET, POST\"", got)
	}

	resp, body = c.do(http.MethodPost, "/files", nil, false)
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("POST /files = %d %s", resp.StatusCode, body)
	}
}

func TestLoginSetsCookieFlags(t *testing.T) {
	env := newTestEnv(t)
	c := env.client()
	c.do(http.MethodGet, "/health", nil, false)
	raw, _ := json.Marshal(map[string]string{"username": "admin", "password": env.password})
	req, _ := http.NewRequest(http.MethodPost, env.server.URL+"/api/v1/auth/login", bytes.NewReader(raw))
	req.Header.Set("Origin", env.server.URL)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-CSRF-Token", c.csrfToken())
	resp, err := c.http.Do(req)
	if err != nil {
		t.Fatalf("post login: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("login = %d", resp.StatusCode)
	}
	seen := map[string]*http.Cookie{}
	for _, ck := range resp.Cookies() {
		seen[ck.Name] = ck
	}
	sess, ok := seen[sessionCookieName]
	if !ok || !sess.HttpOnly {
		t.Errorf("session cookie missing or not HttpOnly: %+v", sess)
	}
	csrf, ok := seen[csrfCookieName]
	if !ok || csrf.HttpOnly {
		t.Errorf("csrf cookie missing or HttpOnly (SPA must read it): %+v", csrf)
	}
	if sess.SameSite != http.SameSiteLaxMode || csrf.SameSite != http.SameSiteLaxMode {
		t.Errorf("cookies must be SameSite=Lax")
	}
}

func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}

func shaHex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

type initUploadT struct {
	UploadID      string `json:"upload_id"`
	PartSize      int64  `json:"part_size"`
	PartsTotal    int64  `json:"parts_total"`
	DedupedNodeID *int64 `json:"deduped_node_id"`
}

type nodeT struct {
	ID   int64  `json:"id"`
	Kind string `json:"kind"`
	Name string `json:"name"`
	Size int64  `json:"size"`
}

// TestUploadDownloadFlow exercises the whole M1 vertical: init -> part ->
// complete -> list -> download (Range/ETag/HEAD) -> instant upload.
func TestUploadDownloadFlow(t *testing.T) {
	env := newTestEnv(t)
	c := env.client()
	c.login("admin", env.password)

	// init
	resp, body := c.do(http.MethodPost, "/api/v1/uploads",
		map[string]any{"parent": 0, "name": "note.txt", "size": 5}, true)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("init: %d %s", resp.StatusCode, body)
	}
	init := decodeJSONT[initUploadT](t, body)
	if init.UploadID == "" || init.PartsTotal != 1 {
		t.Fatalf("unexpected init: %+v", init)
	}

	// part
	resp, body = c.doRaw(http.MethodPut, "/api/v1/uploads/"+init.UploadID+"/parts/0",
		[]byte("hello"), "application/octet-stream", true, nil)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("put part: %d %s", resp.StatusCode, body)
	}

	// complete
	resp, body = c.do(http.MethodPost, "/api/v1/uploads/"+init.UploadID+"/complete",
		map[string]any{"parts": []map[string]any{{"part_no": 0, "sha256": shaHex([]byte("hello"))}}}, true)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("complete: %d %s", resp.StatusCode, body)
	}
	node := decodeJSONT[nodeT](t, body)
	if node.ID == 0 || node.Name != "note.txt" || node.Size != 5 {
		t.Fatalf("unexpected node: %+v", node)
	}

	// list shows the file
	_, body = c.do(http.MethodGet, "/api/v1/nodes", nil, false)
	list := decodeJSONT[listNodesResponseT](t, body)
	if list.Total != 1 || len(list.Items) != 1 || list.Items[0].Kind != "file" {
		t.Fatalf("list after upload: %+v", list)
	}

	// download: full body, ETag, HEAD
	path := "/api/v1/nodes/" + itoa(node.ID) + "/content"
	resp, body = c.do(http.MethodGet, path, nil, false)
	if resp.StatusCode != http.StatusOK || string(body) != "hello" {
		t.Fatalf("download: %d %q", resp.StatusCode, body)
	}
	if got := resp.Header.Get("ETag"); got != `"`+shaHex([]byte("hello"))+`"` {
		t.Fatalf("etag = %q", got)
	}
	if got := resp.Header.Get("Content-Type"); !strings.HasPrefix(got, "text/plain") {
		t.Fatalf("content type = %q", got)
	}

	// range request
	resp, body = c.doRaw(http.MethodGet, path, nil, "", false, map[string]string{"Range": "bytes=1-3"})
	if resp.StatusCode != http.StatusPartialContent || string(body) != "ell" {
		t.Fatalf("range: %d %q", resp.StatusCode, body)
	}
	if got := resp.Header.Get("Content-Range"); !strings.HasPrefix(got, "bytes 1-3/5") {
		t.Fatalf("content-range = %q", got)
	}

	// if-none-match
	resp, _ = c.doRaw(http.MethodGet, path, nil, "", false,
		map[string]string{"If-None-Match": `"` + shaHex([]byte("hello")) + `"`})
	if resp.StatusCode != http.StatusNotModified {
		t.Fatalf("if-none-match: %d", resp.StatusCode)
	}

	// HEAD has no body
	resp, body = c.doRaw(http.MethodHead, path, nil, "", false, nil)
	if resp.StatusCode != http.StatusOK || len(body) != 0 {
		t.Fatalf("head: %d %d bytes", resp.StatusCode, len(body))
	}

	// quota shows the bytes
	_, body = c.do(http.MethodGet, "/api/v1/auth/me", nil, false)
	me := decodeJSONT[meResponse](t, body)
	_ = me

	// instant upload of identical content creates a second node, no bytes
	resp, body = c.do(http.MethodPost, "/api/v1/uploads",
		map[string]any{"parent": 0, "name": "copy.txt", "size": 5, "sha256": shaHex([]byte("hello"))}, true)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("instant init: %d %s", resp.StatusCode, body)
	}
	instant := decodeJSONT[initUploadT](t, body)
	if instant.DedupedNodeID == nil {
		t.Fatalf("expected deduped node, got %+v", instant)
	}
	_, body = c.do(http.MethodGet, "/api/v1/nodes", nil, false)
	list = decodeJSONT[listNodesResponseT](t, body)
	if list.Total != 2 {
		t.Fatalf("total after dedup = %d", list.Total)
	}

	// completing a finished upload is idempotent and returns the same node
	resp, body = c.do(http.MethodPost, "/api/v1/uploads/"+init.UploadID+"/complete",
		map[string]any{"parts": []any{}}, true)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("second complete: %d %s", resp.StatusCode, body)
	}
	if again := decodeJSONT[nodeT](t, body); again.ID != node.ID {
		t.Fatalf("second complete node = %+v, want %d", again, node.ID)
	}
}

func TestUploadValidationAndMethodErrors(t *testing.T) {
	env := newTestEnv(t)
	c := env.client()
	c.login("admin", env.password)

	// anonymous upload is 401 once the CSRF cookie exists (403 without it)
	anon := env.client()
	anon.do(http.MethodGet, "/health", nil, false)
	resp, _ := anon.do(http.MethodPost, "/api/v1/uploads",
		map[string]any{"parent": 0, "name": "x", "size": 1}, true)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("anonymous init: %d", resp.StatusCode)
	}

	// oversized logical file / part bounds
	resp, body := c.do(http.MethodPost, "/api/v1/uploads",
		map[string]any{"parent": 0, "name": "huge.bin", "size": int64(domainfiles.MaxFileSize) + 1}, true)
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversize init: %d %s", resp.StatusCode, body)
	}

	// checksum mismatch on complete
	resp, body = c.do(http.MethodPost, "/api/v1/uploads",
		map[string]any{"parent": 0, "name": "bad.bin", "size": 5, "sha256": shaHex([]byte("hello"))}, true)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("init: %d %s", resp.StatusCode, body)
	}
	bad := decodeJSONT[initUploadT](t, body)
	c.doRaw(http.MethodPut, "/api/v1/uploads/"+bad.UploadID+"/parts/0",
		[]byte("HELLO"), "application/octet-stream", true, nil)
	resp, body = c.do(http.MethodPost, "/api/v1/uploads/"+bad.UploadID+"/complete",
		map[string]any{"parts": []any{}}, true)
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("checksum complete: %d %s", resp.StatusCode, body)
	}
	if got := decodeAPIError(t, body).Error.Code; got != "checksum_mismatch" {
		t.Fatalf("checksum code = %q", got)
	}

	// out of range part numbers are rejected while the upload is still open
	resp, body = c.doRaw(http.MethodPut, "/api/v1/uploads/"+bad.UploadID+"/parts/99",
		[]byte("x"), "application/octet-stream", true, nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("bad part: %d %s", resp.StatusCode, body)
	}

	// a part with the wrong length is rejected (part 0 expects 5 bytes)
	resp, body = c.doRaw(http.MethodPut, "/api/v1/uploads/"+bad.UploadID+"/parts/0",
		[]byte("hi"), "application/octet-stream", true, nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("short part: %d %s", resp.StatusCode, body)
	}

	// cancel releases and is idempotent
	resp, _ = c.do(http.MethodDelete, "/api/v1/uploads/"+bad.UploadID, nil, true)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("cancel: %d", resp.StatusCode)
	}
	resp, _ = c.do(http.MethodDelete, "/api/v1/uploads/"+bad.UploadID, nil, true)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("second cancel: %d", resp.StatusCode)
	}

	// wrong method on a known dynamic API path: 405 with Allow
	resp, body = c.doRaw(http.MethodGet, "/api/v1/uploads/"+bad.UploadID+"/parts/0", nil, "", false, nil)
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("wrong method: %d %s", resp.StatusCode, body)
	}
	if got := resp.Header.Get("Allow"); got != "PUT" {
		t.Fatalf("allow = %q", got)
	}

	// unknown node download is 404
	resp, _ = c.do(http.MethodGet, "/api/v1/nodes/999999/content", nil, false)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown download: %d", resp.StatusCode)
	}
}
