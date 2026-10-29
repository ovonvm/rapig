package httpx

import (
	"net/http"
	"testing"
)

// TestCopyEndpoint covers POST /nodes/{id}/copy: explicit target/name, the
// automatic "X - 副本" name and the listing.
func TestCopyEndpoint(t *testing.T) {
	env := newTestEnv(t)
	c := env.client()
	c.login("admin", env.password)

	resp, body := c.do(http.MethodPost, "/api/v1/nodes", map[string]any{
		"parent": 0, "name": "源目录", "kind": "dir",
	}, true)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create source: %d %s", resp.StatusCode, body)
	}
	src := decodeJSONT[createdNode](t, body)

	resp, body = c.do(http.MethodPost, "/api/v1/nodes", map[string]any{
		"parent": src.ID, "name": "子目录", "kind": "dir",
	}, true)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create child: %d %s", resp.StatusCode, body)
	}

	// Copy the source under its own parent with a custom name.
	resp, body = c.do(http.MethodPost, "/api/v1/nodes/"+itoa(src.ID)+"/copy",
		map[string]any{"parent": 0, "name": "副本"}, true)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("copy explicit: %d %s", resp.StatusCode, body)
	}
	copy1 := decodeJSONT[createdNode](t, body)
	if copy1.Name != "副本" || copy1.ParentID != 0 {
		t.Fatalf("copy1 = %+v", copy1)
	}

	// A second default copy gets the automatic unique name.
	resp, body = c.do(http.MethodPost, "/api/v1/nodes/"+itoa(src.ID)+"/copy", nil, true)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("copy default: %d %s", resp.StatusCode, body)
	}
	copy2 := decodeJSONT[createdNode](t, body)
	if copy2.Name != "源目录 - 副本" {
		t.Fatalf("copy2 name = %q", copy2.Name)
	}

	// Children of the copy are duplicated too.
	resp, body = c.do(http.MethodGet, "/api/v1/nodes?parent="+itoa(copy1.ID), nil, false)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list copy: %d %s", resp.StatusCode, body)
	}
	list := decodeJSONT[struct {
		Items []createdNode `json:"items"`
		Total int           `json:"total"`
	}](t, body)
	if list.Total != 1 || list.Items[0].Name != "子目录" {
		t.Fatalf("copy children = %+v", list)
	}

	// A missing source is a 404 (no existence leak).
	resp, body = c.do(http.MethodPost, "/api/v1/nodes/999999/copy", nil, true)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("copy missing: %d %s", resp.StatusCode, body)
	}
}
