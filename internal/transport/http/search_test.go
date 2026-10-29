package httpx

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// TestSearchEndpoint covers GET /nodes/search: validation, auth, name search
// and the API's parent=0 convention for direct children of the root.
func TestSearchEndpoint(t *testing.T) {
	env := newTestEnv(t)
	c := env.client()
	c.login("admin", env.password)

	for _, name := range []string{"Alpha", "测试目录"} {
		resp, body := c.do(http.MethodPost, "/api/v1/nodes", map[string]any{
			"parent": 0, "name": name, "kind": "dir",
		}, true)
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("create %q: %d %s", name, resp.StatusCode, body)
		}
	}

	search := func(q string) (int, listNodesResponse) {
		t.Helper()
		resp, body := c.do(http.MethodGet, "/api/v1/nodes/search?q="+url.QueryEscape(q), nil, false)
		var out listNodesResponse
		if resp.StatusCode == http.StatusOK {
			out = decodeJSONT[listNodesResponse](t, body)
		}
		return resp.StatusCode, out
	}

	// ASCII search (folded) finds the directory and maps root children to 0.
	code, out := search("alpha")
	if code != http.StatusOK || out.Total != 1 || len(out.Items) != 1 {
		t.Fatalf("search alpha: code=%d total=%d", code, out.Total)
	}
	item := out.Items[0]
	if item.Name != "Alpha" || item.Kind != "dir" || item.ParentID != 0 {
		t.Fatalf("search alpha item = %+v", item)
	}

	// CJK search through the trigram index.
	if code, out = search("测试"); code != http.StatusOK || out.Total != 1 || out.Items[0].Name != "测试目录" {
		t.Fatalf("search CJK: code=%d out=%+v", code, out)
	}

	// One-rune queries fall back to the LIKE scan (trigram needs three runes).
	if code, out = search("a"); code != http.StatusOK || out.Total != 1 || out.Items[0].Name != "Alpha" {
		t.Fatalf("search short one-rune: code=%d total=%d", code, out.Total)
	}
	resp, body := c.do(http.MethodGet, "/api/v1/nodes/search?q=alpha&type=file", nil, false)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("search type=file: %d %s", resp.StatusCode, body)
	}
	if got := decodeJSONT[listNodesResponse](t, body).Total; got != 0 {
		t.Fatalf("search type=file total=%d want 0", got)
	}

	// Validation and auth.
	for _, path := range []string{
		"/api/v1/nodes/search?q=",
		"/api/v1/nodes/search?q=" + strings.Repeat("a", 101),
		"/api/v1/nodes/search?q=alpha&type=bogus",
		"/api/v1/nodes/search?q=alpha&limit=0",
		"/api/v1/nodes/search?q=alpha&limit=201",
		"/api/v1/nodes/search?q=alpha&limit=abc",
	} {
		resp, body := c.do(http.MethodGet, path, nil, false)
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("%s: status=%d body=%s", path, resp.StatusCode, body)
		}
	}

	anon := env.client()
	resp, _ = anon.do(http.MethodGet, "/api/v1/nodes/search?q=alpha", nil, false)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("anonymous search: %d", resp.StatusCode)
	}

	// Wrong method is rejected by the method mux via the API fallback.
	resp, _ = c.do(http.MethodPost, "/api/v1/nodes/search", map[string]any{}, true)
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("POST search: %d", resp.StatusCode)
	}
}
