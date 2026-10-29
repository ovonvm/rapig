package httpx

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	transfersvc "rapig/internal/app/transfers"
	"rapig/internal/infra/qb"
)

const httpTestHash = "0123456789abcdef0123456789abcdef01234567"

// fakeQBHTTP is the smallest qBittorrent WebUI stub: login, version and add.
func fakeQBHTTP(t *testing.T) *qb.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/auth/login":
			http.SetCookie(w, &http.Cookie{Name: "SID", Value: "sid", Path: "/"})
			_, _ = io.WriteString(w, "Ok.")
		case "/api/v2/app/version":
			_, _ = io.WriteString(w, "v4.6.0")
		case "/api/v2/torrents/add":
			_, _ = io.WriteString(w, "Ok.")
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	client, err := qb.New(srv.URL, "user", "pass")
	if err != nil {
		t.Fatalf("qb.New: %v", err)
	}
	return client
}

func decodeError(t *testing.T, body []byte) (string, string) {
	t.Helper()
	var e struct {
		Error struct {
			Code   string `json:"code"`
			Reason string `json:"reason"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &e); err != nil {
		t.Fatalf("decode error body %q: %v", body, err)
	}
	return e.Error.Code, e.Error.Reason
}

func TestTransfersHTTP(t *testing.T) {
	env := newTestEnv(t)

	// Anonymous list is rejected before any CSRF handling.
	anon := env.client()
	if resp, _ := anon.do("GET", "/api/v1/transfers", nil, false); resp.StatusCode != 401 {
		t.Fatalf("anonymous list = %d, want 401", resp.StatusCode)
	}

	c := env.client()
	c.login("admin", env.password)

	resp, body := c.do("POST", "/api/v1/transfers", map[string]any{
		"parent": 0, "name": "x.bin", "url": "ftp://example.com/x",
	}, true)
	if resp.StatusCode != 400 {
		t.Fatalf("invalid url = %d (%s), want 400", resp.StatusCode, body)
	}

	resp, body = c.do("POST", "/api/v1/transfers", map[string]any{
		"parent": 0, "name": "x.bin", "url": "http://example.com/x.bin",
	}, true)
	if resp.StatusCode != 201 {
		t.Fatalf("create = %d (%s), want 201", resp.StatusCode, body)
	}
	var created struct {
		Transfer struct {
			ID    string `json:"id"`
			State string `json:"state"`
			Kind  string `json:"kind"`
		} `json:"transfer"`
	}
	if err := json.Unmarshal(body, &created); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if created.Transfer.ID == "" || created.Transfer.State != "pending" {
		t.Fatalf("created = %+v", created.Transfer)
	}
	if created.Transfer.Kind != "http" {
		t.Fatalf("legacy create kind = %q, want http", created.Transfer.Kind)
	}

	resp, body = c.do("GET", "/api/v1/transfers", nil, false)
	if resp.StatusCode != 200 {
		t.Fatalf("list = %d", resp.StatusCode)
	}
	var list struct {
		Items []map[string]any `json:"items"`
		Total int              `json:"total"`
	}
	if err := json.Unmarshal(body, &list); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if list.Total != 1 || len(list.Items) != 1 {
		t.Fatalf("list total = %d items = %d", list.Total, len(list.Items))
	}

	for _, step := range []struct {
		method string
		path   string
		want   int
	}{
		{"POST", "/api/v1/transfers/" + created.Transfer.ID + "/pause", 204},
		{"POST", "/api/v1/transfers/" + created.Transfer.ID + "/resume", 204},
		{"DELETE", "/api/v1/transfers/" + created.Transfer.ID, 204},
		{"POST", "/api/v1/transfers/missing/pause", 404},
	} {
		resp, body := c.do(step.method, step.path, nil, true)
		if resp.StatusCode != step.want {
			t.Fatalf("%s %s = %d (%s), want %d", step.method, step.path, resp.StatusCode, body, step.want)
		}
	}

	resp, body = c.do("GET", "/api/v1/transfers", nil, false)
	if resp.StatusCode != 200 {
		t.Fatalf("final list = %d", resp.StatusCode)
	}
	if err := json.Unmarshal(body, &list); err != nil {
		t.Fatalf("decode final list: %v", err)
	}
	if list.Total != 0 {
		t.Fatalf("after delete total = %d, want 0", list.Total)
	}
}

func TestTransfersQBStatusWhenDisabled(t *testing.T) {
	env := newTestEnv(t)

	anon := env.client()
	if resp, _ := anon.do("GET", "/api/v1/transfers/qb/status", nil, false); resp.StatusCode != 401 {
		t.Fatalf("anonymous status = %d, want 401", resp.StatusCode)
	}

	c := env.client()
	c.login("admin", env.password)

	resp, body := c.do("GET", "/api/v1/transfers/qb/status", nil, false)
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d (%s)", resp.StatusCode, body)
	}
	var status struct {
		Enabled bool `json:"enabled"`
	}
	if err := json.Unmarshal(body, &status); err != nil {
		t.Fatalf("decode status: %v", err)
	}
	if status.Enabled {
		t.Fatal("enabled = true without qBittorrent configuration")
	}

	// A wrong method on the status route is a 405, not an unknown-route 404.
	if resp, body := c.do("POST", "/api/v1/transfers/qb/status", nil, true); resp.StatusCode != 405 {
		t.Fatalf("POST status = %d (%s), want 405", resp.StatusCode, body)
	}

	resp, body = c.do("POST", "/api/v1/transfers", map[string]any{
		"parent": 0, "kind": "qb", "magnet": "magnet:?xt=urn:btih:" + httpTestHash,
	}, true)
	if resp.StatusCode != 503 {
		t.Fatalf("disabled qb create = %d (%s), want 503", resp.StatusCode, body)
	}
	if code, _ := decodeError(t, body); code != "qbt_unavailable" {
		t.Fatalf("error code = %q, want qbt_unavailable", code)
	}

	resp, body = c.do("POST", "/api/v1/transfers", map[string]any{
		"parent": 0, "kind": "ftp",
	}, true)
	if resp.StatusCode != 400 {
		t.Fatalf("invalid kind = %d (%s), want 400", resp.StatusCode, body)
	}
	if code, reason := decodeError(t, body); code != "bad_request" || reason != "invalid_kind" {
		t.Fatalf("invalid kind error = %s/%s", code, reason)
	}
}

func TestTransfersQBCreateWithEnabledClient(t *testing.T) {
	env := newTestEnvWith(t, transfersvc.Options{QB: fakeQBHTTP(t), DataDir: t.TempDir()})
	c := env.client()
	c.login("admin", env.password)

	resp, body := c.do("GET", "/api/v1/transfers/qb/status", nil, false)
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d (%s)", resp.StatusCode, body)
	}
	var status struct {
		Enabled bool `json:"enabled"`
	}
	if err := json.Unmarshal(body, &status); err != nil {
		t.Fatalf("decode status: %v", err)
	}
	if !status.Enabled {
		t.Fatal("enabled = false with a configured client")
	}

	magnet := "magnet:?xt=urn:btih:" + httpTestHash + "&dn=demo.bin"
	resp, body = c.do("POST", "/api/v1/transfers", map[string]any{
		"parent": 0, "kind": "qb", "magnet": magnet,
	}, true)
	if resp.StatusCode != 201 {
		t.Fatalf("qb create = %d (%s), want 201", resp.StatusCode, body)
	}
	var created struct {
		Transfer struct {
			ID        string `json:"id"`
			Kind      string `json:"kind"`
			Name      string `json:"name"`
			SourceURL string `json:"source_url"`
			State     string `json:"state"`
			Size      int64  `json:"size"`
		} `json:"transfer"`
	}
	if err := json.Unmarshal(body, &created); err != nil {
		t.Fatalf("decode qb create: %v", err)
	}
	tr := created.Transfer
	if tr.ID == "" || tr.Kind != "qb" || tr.State != "pending" || tr.Size != 0 {
		t.Fatalf("qb transfer = %+v", tr)
	}
	if tr.Name != "demo.bin" {
		t.Fatalf("name = %q, want the magnet display name", tr.Name)
	}
	if tr.SourceURL != magnet {
		t.Fatalf("source_url = %q", tr.SourceURL)
	}

	resp, body = c.do("POST", "/api/v1/transfers", map[string]any{
		"parent": 0, "kind": "qb", "magnet": "magnet:?xt=urn:btih:short",
	}, true)
	if resp.StatusCode != 400 {
		t.Fatalf("invalid magnet = %d (%s), want 400", resp.StatusCode, body)
	}
	if code, reason := decodeError(t, body); code != "bad_request" || reason != "invalid_magnet" {
		t.Fatalf("invalid magnet error = %s/%s", code, reason)
	}

	// Missing magnet behaves like an invalid one.
	resp, body = c.do("POST", "/api/v1/transfers", map[string]any{
		"parent": 0, "kind": "qb",
	}, true)
	if resp.StatusCode != 400 {
		t.Fatalf("missing magnet = %d (%s), want 400", resp.StatusCode, body)
	}
}
