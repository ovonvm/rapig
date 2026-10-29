package httpx

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

// createDirT creates a directory through the API and returns its node.
func createDirT(t *testing.T, c *apiClient, parent int64, name string) nodeT {
	t.Helper()
	resp, body := c.do(http.MethodPost, "/api/v1/nodes",
		map[string]any{"parent": parent, "name": name, "kind": "dir"}, true)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create dir %q: %d %s", name, resp.StatusCode, body)
	}
	return decodeJSONT[nodeT](t, body)
}

// uploadT uploads one small file through init/part/complete.
func uploadT(t *testing.T, c *apiClient, parent int64, name string, content []byte) nodeT {
	t.Helper()
	resp, body := c.do(http.MethodPost, "/api/v1/uploads",
		map[string]any{"parent": parent, "name": name, "size": len(content)}, true)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("init %q: %d %s", name, resp.StatusCode, body)
	}
	init := decodeJSONT[initUploadT](t, body)
	resp, body = c.doRaw(http.MethodPut, "/api/v1/uploads/"+init.UploadID+"/parts/0",
		content, "application/octet-stream", true, nil)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("put part %q: %d %s", name, resp.StatusCode, body)
	}
	resp, body = c.do(http.MethodPost, "/api/v1/uploads/"+init.UploadID+"/complete",
		map[string]any{"parts": []map[string]any{{"part_no": 0, "sha256": shaHex(content)}}}, true)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("complete %q: %d %s", name, resp.StatusCode, body)
	}
	return decodeJSONT[nodeT](t, body)
}

type shareT struct {
	ID            string `json:"id"`
	Token         string `json:"token"`
	URL           string `json:"url"`
	NodeID        int64  `json:"node_id"`
	Name          string `json:"name"`
	Kind          string `json:"kind"`
	ItemCount     int    `json:"item_count"`
	HasPassword   bool   `json:"has_password"`
	MaxDownloads  int64  `json:"max_downloads"`
	DownloadCount int64  `json:"download_count"`
	Active        bool   `json:"active"`
}

type publicShareT struct {
	Locked        bool   `json:"locked"`
	NodeID        int64  `json:"node_id"`
	Name          string `json:"name"`
	Kind          string `json:"kind"`
	ItemCount     int    `json:"item_count"`
	HasPassword   bool   `json:"has_password"`
	DownloadsLeft *int64 `json:"downloads_left"`
}

type publicListT struct {
	Items []struct {
		ID   int64  `json:"id"`
		Name string `json:"name"`
		Kind string `json:"kind"`
	} `json:"items"`
	Total int `json:"total"`
}

func sharePayload(t *testing.T, body []byte) shareT {
	t.Helper()
	var out struct {
		Share shareT `json:"share"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("decode share: %v (%s)", err, body)
	}
	return out.Share
}

func publicPayload(t *testing.T, body []byte) publicShareT {
	t.Helper()
	var out struct {
		Share publicShareT `json:"share"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("decode public share: %v (%s)", err, body)
	}
	return out.Share
}

// TestShareOwnerAndPublicFileFlow covers create/list/download/limit/revoke.
func TestShareOwnerAndPublicFileFlow(t *testing.T) {
	env := newTestEnv(t)
	owner := env.client()
	owner.login("admin", env.password)
	file := uploadT(t, owner, 0, "share.txt", []byte("hello share"))

	expiresIn := int64(3600)
	resp, body := owner.do(http.MethodPost, "/api/v1/shares",
		map[string]any{"node_id": file.ID, "expires_in": expiresIn, "max_downloads": 2}, true)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create share: %d %s", resp.StatusCode, body)
	}
	sh := sharePayload(t, body)
	if sh.URL != "/s/"+sh.Token || sh.HasPassword || sh.MaxDownloads != 2 {
		t.Fatalf("unexpected share: %+v", sh)
	}

	resp, body = owner.do(http.MethodGet, "/api/v1/shares", nil, false)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list shares: %d %s", resp.StatusCode, body)
	}
	var list struct {
		Items []shareT `json:"items"`
		Total int      `json:"total"`
	}
	if err := json.Unmarshal(body, &list); err != nil || list.Total != 1 || list.Items[0].ID != sh.ID {
		t.Fatalf("share list: %v %s", err, body)
	}

	anon := env.client()
	base := "/api/v1/public/shares/" + sh.Token
	resp, body = anon.do(http.MethodGet, base, nil, false)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("public meta: %d %s", resp.StatusCode, body)
	}
	pub := publicPayload(t, body)
	if pub.Locked || pub.Name != "share.txt" || pub.Kind != "file" {
		t.Fatalf("public meta = %+v", pub)
	}
	if pub.DownloadsLeft == nil || *pub.DownloadsLeft != 2 {
		t.Fatalf("downloads_left = %v", pub.DownloadsLeft)
	}

	content := base + "/nodes/" + itoa(file.ID) + "/content"
	for i := 0; i < 2; i++ {
		resp, body = anon.do(http.MethodGet, content, nil, false)
		if resp.StatusCode != http.StatusOK || string(body) != "hello share" {
			t.Fatalf("download %d: %d %q", i, resp.StatusCode, body)
		}
	}
	resp, body = anon.do(http.MethodGet, content, nil, false)
	if resp.StatusCode != http.StatusGone || !strings.Contains(string(body), "share_exhausted") {
		t.Fatalf("download over budget: %d %s", resp.StatusCode, body)
	}

	resp, body = owner.do(http.MethodDelete, "/api/v1/shares/"+sh.ID, nil, true)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("revoke: %d %s", resp.StatusCode, body)
	}
	resp, _ = anon.do(http.MethodGet, base, nil, false)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("meta after revoke = %d", resp.StatusCode)
	}
}

// TestShareDirectoryAndPasswordGate covers folder shares and password tickets.
func TestShareDirectoryAndPasswordGate(t *testing.T) {
	env := newTestEnv(t)
	owner := env.client()
	owner.login("admin", env.password)
	dir := createDirT(t, owner, 0, "album")
	innerDir := createDirT(t, owner, dir.ID, "2024")
	inner := uploadT(t, owner, innerDir.ID, "pic.txt", []byte("pixels"))
	outside := uploadT(t, owner, 0, "private.txt", []byte("secret"))

	resp, body := owner.do(http.MethodPost, "/api/v1/shares",
		map[string]any{"node_id": dir.ID, "password": "abcd1234"}, true)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create dir share: %d %s", resp.StatusCode, body)
	}
	sh := sharePayload(t, body)
	if sh.Kind != "dir" || !sh.HasPassword {
		t.Fatalf("dir share = %+v", sh)
	}

	anon := env.client()
	base := "/api/v1/public/shares/" + sh.Token
	anon.do(http.MethodGet, "/health", nil, false) // installs the csrf cookie
	resp, body = anon.do(http.MethodGet, base, nil, false)
	if resp.StatusCode != http.StatusOK || !publicPayload(t, body).Locked {
		t.Fatalf("locked meta: %d %s", resp.StatusCode, body)
	}
	resp, _ = anon.do(http.MethodPost, base+"/verify", map[string]any{"password": "wrong-pass"}, true)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("wrong password = %d", resp.StatusCode)
	}
	resp, body = anon.do(http.MethodPost, base+"/verify", map[string]any{"password": "abcd1234"}, true)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("verify: %d %s", resp.StatusCode, body)
	}
	resp, body = anon.do(http.MethodGet, base, nil, false)
	pub := publicPayload(t, body)
	if resp.StatusCode != http.StatusOK || pub.Locked || pub.Name != "album" {
		t.Fatalf("unlocked meta: %d %+v %s", resp.StatusCode, pub, body)
	}

	resp, body = anon.do(http.MethodGet, base+"/nodes", nil, false)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("public nodes: %d %s", resp.StatusCode, body)
	}
	var lvl publicListT
	if err := json.Unmarshal(body, &lvl); err != nil || lvl.Total != 1 || lvl.Items[0].Name != "2024" {
		t.Fatalf("public nodes = %+v %s", lvl, body)
	}
	resp, body = anon.do(http.MethodGet, base+"/nodes?parent="+itoa(innerDir.ID), nil, false)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("public inner nodes: %d %s", resp.StatusCode, body)
	}
	resp, body = anon.do(http.MethodGet, base+"/nodes/"+itoa(inner.ID)+"/content", nil, false)
	if resp.StatusCode != http.StatusOK || string(body) != "pixels" {
		t.Fatalf("public inner content: %d %q", resp.StatusCode, body)
	}
	resp, _ = anon.do(http.MethodGet, base+"/nodes/"+itoa(outside.ID)+"/content", nil, false)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("content outside subtree = %d", resp.StatusCode)
	}
}

// TestShareRequiresOwnedNode checks the owner boundary on creation.
func TestShareRequiresOwnedNode(t *testing.T) {
	env := newTestEnv(t)
	c := env.client()
	c.login("admin", env.password)
	resp, _ := c.do(http.MethodPost, "/api/v1/shares", map[string]any{"node_id": 999999}, true)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("foreign node share = %d", resp.StatusCode)
	}
	resp, _ = c.do(http.MethodGet, "/api/v1/shares/not-a-share", nil, false)
	_ = resp
}

var _ io.Reader

// TestMergedShareAndLifecycleHTTP covers merged node_ids shares, the status
// filter, restore and purge.
func TestMergedShareAndLifecycleHTTP(t *testing.T) {
	env := newTestEnv(t)
	owner := env.client()
	owner.login("admin", env.password)
	a := uploadT(t, owner, 0, "a.txt", []byte("aaa"))
	b := uploadT(t, owner, 0, "b.txt", []byte("bbb"))
	dir := createDirT(t, owner, 0, "keep")

	resp, body := owner.do(http.MethodPost, "/api/v1/shares",
		map[string]any{"node_ids": []int64{a.ID, b.ID, dir.ID}}, true)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create merged: %d %s", resp.StatusCode, body)
	}
	sh := sharePayload(t, body)
	if sh.Kind != "multi" || sh.ItemCount != 3 {
		t.Fatalf("merged share = %+v", sh)
	}

	anon := env.client()
	base := "/api/v1/public/shares/" + sh.Token
	resp, body = anon.do(http.MethodGet, base, nil, false)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("merged meta: %d %s", resp.StatusCode, body)
	}
	pub := publicPayload(t, body)
	if pub.Kind != "multi" || pub.ItemCount != 3 {
		t.Fatalf("merged public meta = %+v", pub)
	}
	resp, body = anon.do(http.MethodGet, base+"/nodes", nil, false)
	var lvl publicListT
	if err := json.Unmarshal(body, &lvl); resp.StatusCode != http.StatusOK || err != nil || lvl.Total != 3 {
		t.Fatalf("merged nodes = %d %+v %s", resp.StatusCode, lvl, body)
	}
	resp, body = anon.do(http.MethodGet, base+"/nodes/"+itoa(a.ID)+"/content", nil, false)
	if resp.StatusCode != http.StatusOK || string(body) != "aaa" {
		t.Fatalf("merged download: %d %q", resp.StatusCode, body)
	}

	resp, body = owner.do(http.MethodGet, "/api/v1/shares?status=active", nil, false)
	var list struct {
		Items []shareT `json:"items"`
		Total int      `json:"total"`
	}
	if err := json.Unmarshal(body, &list); resp.StatusCode != http.StatusOK || err != nil || list.Total != 1 {
		t.Fatalf("active list = %d %s", resp.StatusCode, body)
	}
	if resp, _ := owner.do(http.MethodGet, "/api/v1/shares?status=bogus", nil, false); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("invalid status = %d", resp.StatusCode)
	}

	if resp, body := owner.do(http.MethodDelete, "/api/v1/shares/"+sh.ID, nil, true); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("revoke: %d %s", resp.StatusCode, body)
	}
	resp, body = owner.do(http.MethodGet, "/api/v1/shares?status=revoked", nil, false)
	if err := json.Unmarshal(body, &list); resp.StatusCode != http.StatusOK || err != nil || list.Total != 1 {
		t.Fatalf("revoked list = %d %s", resp.StatusCode, body)
	}
	if resp, _ := anon.do(http.MethodGet, base, nil, false); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("meta after revoke = %d", resp.StatusCode)
	}

	resp, body = owner.do(http.MethodPost, "/api/v1/shares/"+sh.ID+"/restore", nil, true)
	if resp.StatusCode != http.StatusOK || !sharePayload(t, body).Active {
		t.Fatalf("restore = %d %s", resp.StatusCode, body)
	}
	if resp, _ := anon.do(http.MethodGet, base, nil, false); resp.StatusCode != http.StatusOK {
		t.Fatalf("meta after restore = %d", resp.StatusCode)
	}

	if resp, body := owner.do(http.MethodDelete, "/api/v1/shares/"+sh.ID, nil, true); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("revoke again: %d %s", resp.StatusCode, body)
	}
	if resp, body := owner.do(http.MethodDelete, "/api/v1/shares/"+sh.ID+"?purge=1", nil, true); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("purge: %d %s", resp.StatusCode, body)
	}
	resp, body = owner.do(http.MethodGet, "/api/v1/shares?status=all", nil, false)
	if err := json.Unmarshal(body, &list); resp.StatusCode != http.StatusOK || err != nil || list.Total != 0 {
		t.Fatalf("after purge = %d %s", resp.StatusCode, body)
	}
}
