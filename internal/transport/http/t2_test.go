package httpx

import (
	"encoding/json"
	"net/http"
	"testing"
)

type createdNode struct {
	ID       int64  `json:"id"`
	ParentID int64  `json:"parent_id"`
	Name     string `json:"name"`
	Path     string `json:"path"`
	Revision string `json:"revision"`
}
type trashBody struct {
	Items []struct {
		ID               int64  `json:"id"`
		Name             string `json:"name"`
		OriginalParentID int64  `json:"original_parent_id"`
		TrashedAt        string `json:"trashed_at"`
		RemainingDays    int    `json:"remaining_days"`
	} `json:"items"`
	Total int `json:"total"`
}

// TestTrashAndMoveHTTP covers the M2 endpoints end to end: rename with the
// revision token, soft delete, trash listing, restore, purge and empty.
func TestTrashAndMoveHTTP(t *testing.T) {
	env := newTestEnv(t)
	c := env.client()
	c.login("admin", env.password)

	resp, body := c.do("POST", "/api/v1/nodes", map[string]any{"parent": 0, "name": "A", "kind": "dir"}, true)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create A: %d %s", resp.StatusCode, body)
	}
	var a createdNode
	if err := json.Unmarshal(body, &a); err != nil {
		t.Fatalf("decode A: %v", err)
	}

	// Rename with the revision returned by create.
	resp, body = c.do("PATCH", "/api/v1/nodes/"+itoa(a.ID), map[string]any{"name": "A2", "revision": a.Revision}, true)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("rename: %d %s", resp.StatusCode, body)
	}
	var renamed createdNode
	if err := json.Unmarshal(body, &renamed); err != nil {
		t.Fatalf("decode renamed: %v", err)
	}
	if renamed.Name != "A2" {
		t.Fatalf("renamed = %+v", renamed)
	}

	// The old revision is now stale.
	resp, body = c.do("PATCH", "/api/v1/nodes/"+itoa(a.ID), map[string]any{"name": "A3", "revision": a.Revision}, true)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("stale rename: %d %s", resp.StatusCode, body)
	}
	var staleErr struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &staleErr); err != nil || staleErr.Error.Code != "stale_write" {
		t.Fatalf("stale error body = %s", body)
	}

	// Soft delete, then it is out of the normal list and in the trash.
	resp, body = c.do("DELETE", "/api/v1/nodes/"+itoa(a.ID), nil, true)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("trash: %d %s", resp.StatusCode, body)
	}
	_, body = c.do("GET", "/api/v1/nodes?parent=0", nil, false)
	var list struct {
		Total int `json:"total"`
	}
	if err := json.Unmarshal(body, &list); err != nil || list.Total != 0 {
		t.Fatalf("list after trash = %s", body)
	}
	_, body = c.do("GET", "/api/v1/trash", nil, false)
	var trash trashBody
	if err := json.Unmarshal(body, &trash); err != nil {
		t.Fatalf("decode trash: %v", err)
	}
	if trash.Total != 1 || len(trash.Items) != 1 || trash.Items[0].Name != "A2" || trash.Items[0].TrashedAt == "" {
		t.Fatalf("trash body = %s", body)
	}

	// Restore brings it back.
	resp, body = c.do("POST", "/api/v1/nodes/"+itoa(a.ID)+"/restore", nil, true)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("restore: %d %s", resp.StatusCode, body)
	}
	_, body = c.do("GET", "/api/v1/nodes?parent=0", nil, false)
	if err := json.Unmarshal(body, &list); err != nil || list.Total != 1 {
		t.Fatalf("list after restore = %s", body)
	}

	// Empty trash purges it for good.
	resp, body = c.do("DELETE", "/api/v1/nodes/"+itoa(a.ID), nil, true)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("trash again: %d %s", resp.StatusCode, body)
	}
	resp, body = c.do("POST", "/api/v1/trash/empty", nil, true)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("empty: %d %s", resp.StatusCode, body)
	}
	var empty struct {
		Purged int `json:"purged"`
	}
	if err := json.Unmarshal(body, &empty); err != nil || empty.Purged != 1 {
		t.Fatalf("empty body = %s", body)
	}
	_, body = c.do("GET", "/api/v1/trash", nil, false)
	if err := json.Unmarshal(body, &trash); err != nil || trash.Total != 0 {
		t.Fatalf("trash after empty = %s", body)
	}
}

// TestMoveRejectsSelfAndMissingRevision checks the two client mistakes that
// must produce stable codes.
func TestMoveRejectsSelfAndMissingRevision(t *testing.T) {
	env := newTestEnv(t)
	c := env.client()
	c.login("admin", env.password)
	resp, body := c.do("POST", "/api/v1/nodes", map[string]any{"parent": 0, "name": "self", "kind": "dir"}, true)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create: %d %s", resp.StatusCode, body)
	}
	var n createdNode
	if err := json.Unmarshal(body, &n); err != nil {
		t.Fatalf("decode: %v", err)
	}
	resp, body = c.do("PATCH", "/api/v1/nodes/"+itoa(n.ID), map[string]any{"parent": n.ID, "revision": n.Revision}, true)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("move into self: %d %s", resp.StatusCode, body)
	}
	resp, body = c.do("PATCH", "/api/v1/nodes/"+itoa(n.ID), map[string]any{"name": "x"}, true)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("missing revision: %d %s", resp.StatusCode, body)
	}
}
