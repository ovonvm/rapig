package transfers

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"rapig/internal/domain/access"
	domainfiles "rapig/internal/domain/files"
	"rapig/internal/domain/transfer"
	"rapig/internal/infra/fetch"
	"rapig/internal/infra/qb"
	"rapig/internal/platform/apperr"
)

// Test infohashes (40 hex characters, lowercase).
const (
	testHash  = "0123456789abcdef0123456789abcdef01234567"
	hashTwo   = "89abcdef0123456789abcdef0123456789abcdef"
	hashThree = "fedcba9876543210fedcba9876543210fedcba98"
)

func qbMagnet(hash, displayName string) string {
	m := "magnet:?xt=urn:btih:" + hash
	if displayName != "" {
		m += "&dn=" + url.QueryEscape(displayName)
	}
	return m
}

func newQBSvc(t *testing.T) (*env, *fakeQB, *Service) {
	t.Helper()
	e := newEnv(t)
	fake, client := newFakeQB(t)
	svc := New(e.db, e.registry, fetch.New(true), e.log, 2, Options{QB: client, DataDir: e.dir})
	return e, fake, svc
}

// ---- fake qBittorrent ------------------------------------------------------

type fakeQBTorrent struct {
	Hash        string  `json:"hash"`
	Name        string  `json:"name"`
	Size        int64   `json:"size"`
	Progress    float64 `json:"progress"`
	State       string  `json:"state"`
	Category    string  `json:"category"`
	SavePath    string  `json:"save_path"`
	ContentPath string  `json:"content_path"`
}

// qbTorrentFileJSON mirrors qb.TorrentFile for the fake WebUI endpoint.
type qbTorrentFileJSON struct {
	Name     string  `json:"name"`
	Size     int64   `json:"size"`
	Progress float64 `json:"progress"`
	Priority int     `json:"priority"`
}

type fakeQB struct {
	mu        sync.Mutex
	torrents  map[string]*fakeQBTorrent
	files     map[string][]qbTorrentFileJSON
	adds      []url.Values
	pauses    int
	resumes   int
	deletes   int
	deleteArg string
}

func newFakeQB(t *testing.T) (*fakeQB, *qb.Client) {
	t.Helper()
	f := &fakeQB{torrents: map[string]*fakeQBTorrent{}, files: map[string][]qbTorrentFileJSON{}}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	client, err := qb.New(srv.URL, "admin", "secret")
	if err != nil {
		t.Fatalf("qb.New: %v", err)
	}
	return f, client
}

func (f *fakeQB) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.URL.Path == "/api/v2/auth/login" {
		http.SetCookie(w, &http.Cookie{Name: "SID", Value: "sid-test", Path: "/"})
		_, _ = io.WriteString(w, "Ok.")
		return
	}
	if c, err := r.Cookie("SID"); err != nil || c.Value != "sid-test" {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	switch r.URL.Path {
	case "/api/v2/app/version":
		_, _ = io.WriteString(w, "v4.6.0")
	case "/api/v2/torrents/add":
		_ = r.ParseForm()
		f.adds = append(f.adds, r.PostForm)
		hash := hashFromMagnet(r.PostForm.Get("urls"))
		f.torrents[hash] = &fakeQBTorrent{
			Hash:     hash,
			Name:     "demo",
			State:    "downloading",
			Category: r.PostForm.Get("category"),
			SavePath: r.PostForm.Get("savepath"),
		}
		_, _ = io.WriteString(w, "Ok.")
	case "/api/v2/torrents/info":
		seen := splitHashes(r.URL.Query().Get("hashes"))
		out := make([]*fakeQBTorrent, 0, len(seen))
		for _, h := range seen {
			if t, ok := f.torrents[h]; ok {
				out = append(out, t)
			}
		}
		_ = json.NewEncoder(w).Encode(out)
	case "/api/v2/torrents/files":
		list := f.files[r.URL.Query().Get("hash")]
		if list == nil {
			list = []qbTorrentFileJSON{}
		}
		_ = json.NewEncoder(w).Encode(list)
	case "/api/v2/torrents/pause", "/api/v2/torrents/resume":
		if r.URL.Path == "/api/v2/torrents/pause" {
			f.pauses++
		} else {
			f.resumes++
		}
		_ = r.ParseForm()
		for _, h := range splitHashes(r.PostForm.Get("hashes")) {
			if t, ok := f.torrents[h]; ok {
				if r.URL.Path == "/api/v2/torrents/pause" {
					t.State = "pausedDL"
				} else {
					t.State = "downloading"
				}
			}
		}
		_, _ = io.WriteString(w, "Ok.")
	case "/api/v2/torrents/delete":
		f.deletes++
		_ = r.ParseForm()
		f.deleteArg = r.PostForm.Get("hashes") + "|" + r.PostForm.Get("deleteFiles")
		for _, h := range splitHashes(r.PostForm.Get("hashes")) {
			delete(f.torrents, h)
		}
		_, _ = io.WriteString(w, "Ok.")
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func hashFromMagnet(m string) string {
	const prefix = "urn:btih:"
	i := strings.Index(m, prefix)
	if i < 0 {
		return ""
	}
	rest := m[i+len(prefix):]
	if j := strings.IndexAny(rest, "&"); j >= 0 {
		rest = rest[:j]
	}
	return strings.ToLower(rest)
}

func splitHashes(raw string) []string {
	if raw == "" {
		return nil
	}
	return strings.Split(raw, "|")
}

// finish marks a torrent complete, as qBittorrent would after downloading.
func (f *fakeQB) finish(hash string, size int64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if t, ok := f.torrents[hash]; ok {
		t.Progress = 1
		t.Size = size
		t.State = "uploading"
	}
}

func (f *fakeQB) setFiles(hash string, files []qbTorrentFileJSON) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.files[hash] = files
}

func (f *fakeQB) addForms() []url.Values {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]url.Values(nil), f.adds...)
}

func (f *fakeQB) counters() (pauses, resumes, deletes int, deleteArg string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.pauses, f.resumes, f.deletes, f.deleteArg
}

// ---- tests -----------------------------------------------------------------

func TestQBCreateAddPollImport(t *testing.T) {
	e, fake, svc := newQBSvc(t)
	ctx := context.Background()
	magnet := qbMagnet(testHash, "demo.bin")

	tr, err := svc.CreateQB(ctx, e.principal, e.root, "", magnet)
	if err != nil {
		t.Fatalf("CreateQB: %v", err)
	}
	if tr.KindValue() != transfer.KindQB || tr.QBHash != testHash {
		t.Fatalf("created = %+v", tr)
	}
	if tr.QBCategory != fmt.Sprintf("rapig-%d", e.userID) {
		t.Fatalf("category = %q", tr.QBCategory)
	}
	if tr.Name != "demo.bin" {
		t.Fatalf("name = %q, want the dn display name", tr.Name)
	}
	if tr.State != transfer.StatePending || tr.Size != 0 || tr.NodeID != nil {
		t.Fatalf("initial row = %+v", tr)
	}

	if n := svc.RunQB(ctx); n != 1 {
		t.Fatalf("RunQB started %d pollers, want 1", n)
	}
	svc.Wait()

	forms := fake.addForms()
	if len(forms) != 1 {
		t.Fatalf("add calls = %d, want 1", len(forms))
	}
	savePath := svc.qbSavePath(tr)
	if got := forms[0].Get("savepath"); got != savePath {
		t.Fatalf("savepath = %q, want %q", got, savePath)
	}
	if got := forms[0].Get("category"); got != tr.QBCategory {
		t.Fatalf("add category = %q", got)
	}
	if got := forms[0].Get("paused"); got != "false" {
		t.Fatalf("paused = %q, want false", got)
	}
	if got := forms[0].Get("urls"); got != magnet {
		t.Fatalf("urls = %q", got)
	}

	// qBittorrent finishes: the single file appears below the save path.
	content := []byte("qBittorrent payload")
	if err := os.MkdirAll(savePath, 0o755); err != nil {
		t.Fatalf("mkdir save path: %v", err)
	}
	if err := os.WriteFile(filepath.Join(savePath, "demo.bin"), content, 0o644); err != nil {
		t.Fatalf("write payload: %v", err)
	}
	fake.setFiles(testHash, []qbTorrentFileJSON{{Name: "demo.bin", Size: int64(len(content)), Priority: 1}})
	fake.finish(testHash, int64(len(content)))

	if n := svc.RunQB(ctx); n != 1 {
		t.Fatalf("second RunQB started %d pollers", n)
	}
	svc.Wait()

	sc := access.UserScope(e.userID, false)
	got, err := e.db.TransferByID(ctx, sc, tr.ID)
	if err != nil {
		t.Fatalf("load transfer: %v", err)
	}
	if got.State != transfer.StateCompleted {
		t.Fatalf("state = %s (error %q)", got.State, got.Error)
	}
	if got.NodeID == nil {
		t.Fatal("completed qb transfer has no node")
	}
	if got.Size != int64(len(content)) || got.ReceivedBytes != int64(len(content)) {
		t.Fatalf("size = %d received = %d", got.Size, got.ReceivedBytes)
	}
	node, sha, _, err := e.db.FileContent(ctx, sc, *got.NodeID)
	if err != nil {
		t.Fatalf("content: %v", err)
	}
	if node.Name != "demo.bin" {
		t.Fatalf("node name = %q", node.Name)
	}
	if node.ParentID == nil || *node.ParentID != e.root.ID {
		t.Fatalf("node parent = %v, want root %d", node.ParentID, e.root.ID)
	}
	if sha != shaHex(content) {
		t.Fatalf("sha = %q, want %q", sha, shaHex(content))
	}

	pauses, resumes, deletes, deleteArg := fake.counters()
	if pauses != 0 || resumes != 0 {
		t.Fatalf("unexpected pause/resume calls: %d/%d", pauses, resumes)
	}
	if deletes != 1 || deleteArg != testHash+"|true" {
		t.Fatalf("delete calls = %d arg = %q", deletes, deleteArg)
	}
	if _, err := os.Stat(savePath); !os.IsNotExist(err) {
		t.Fatalf("temporary save path still exists: %v", err)
	}
	// Terminal jobs are never polled again.
	if n := svc.RunQB(ctx); n != 0 {
		t.Fatalf("RunQB on a completed job started %d", n)
	}
}

func TestQBImportMultiFileDirectory(t *testing.T) {
	e, fake, svc := newQBSvc(t)
	ctx := context.Background()

	tr, err := svc.CreateQB(ctx, e.principal, e.root, "", qbMagnet(testHash, "multi"))
	if err != nil {
		t.Fatalf("CreateQB: %v", err)
	}
	svc.RunQB(ctx)
	svc.Wait()

	savePath := svc.qbSavePath(tr)
	if err := os.MkdirAll(filepath.Join(savePath, "multi", "sub"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	files := []struct {
		name string
		body string
	}{
		{"multi/a.bin", "aaa"},
		{"multi/sub/b.bin", "bbbb"},
		{"multi/skip.bin", "ccccc"},
	}
	for _, f := range files {
		if err := os.WriteFile(filepath.Join(savePath, filepath.FromSlash(f.name)), []byte(f.body), 0o644); err != nil {
			t.Fatalf("write %s: %v", f.name, err)
		}
	}
	fake.setFiles(testHash, []qbTorrentFileJSON{
		{Name: "multi/a.bin", Size: 3, Priority: 1},
		{Name: "multi/sub/b.bin", Size: 4, Priority: 1},
		{Name: "multi/skip.bin", Size: 5, Priority: 0},
	})
	fake.finish(testHash, 12)

	if n := svc.RunQB(ctx); n != 1 {
		t.Fatalf("RunQB started %d", n)
	}
	svc.Wait()

	sc := access.UserScope(e.userID, false)
	got, err := e.db.TransferByID(ctx, sc, tr.ID)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got.State != transfer.StateCompleted {
		t.Fatalf("state = %s (error %q)", got.State, got.Error)
	}
	if got.NodeID == nil {
		t.Fatal("completed multi-file transfer has no node")
	}
	node, err := e.db.NodeByID(ctx, sc, *got.NodeID)
	if err != nil {
		t.Fatalf("node: %v", err)
	}
	if node.Kind != domainfiles.KindDir || node.Name != "multi" {
		t.Fatalf("top node = %s %q, want dir multi", node.Kind, node.Name)
	}
	children, total, err := e.db.ListChildren(ctx, sc, node.ID, 100, 0)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if total != 2 {
		t.Fatalf("children = %d, want 2 (skipped files excluded)", total)
	}
	names := map[string]bool{}
	for _, c := range children {
		names[c.Name] = true
	}
	if !names["a.bin"] || !names["sub"] || names["skip.bin"] {
		t.Fatalf("children = %v", names)
	}
	if got.Size != 7 || got.ReceivedBytes != 7 {
		t.Fatalf("size = %d received = %d, want 7", got.Size, got.ReceivedBytes)
	}
	if _, _, deletes, deleteArg := fake.counters(); deletes != 1 || deleteArg != testHash+"|true" {
		t.Fatalf("delete calls = %d arg = %q", deletes, deleteArg)
	}
	if _, err := os.Stat(savePath); !os.IsNotExist(err) {
		t.Fatalf("temporary save path still exists: %v", err)
	}
}

func TestQBPauseResumeDeleteForwarding(t *testing.T) {
	e, fake, svc := newQBSvc(t)
	ctx := context.Background()

	tr, err := svc.CreateQB(ctx, e.principal, e.root, "pausable", qbMagnet(testHash, ""))
	if err != nil {
		t.Fatalf("CreateQB: %v", err)
	}
	svc.RunQB(ctx)
	svc.Wait()
	sc := access.UserScope(e.userID, false)

	if err := svc.Pause(ctx, e.principal, tr.ID); err != nil {
		t.Fatalf("Pause: %v", err)
	}
	got, err := e.db.TransferByID(ctx, sc, tr.ID)
	if err != nil {
		t.Fatalf("load paused: %v", err)
	}
	if got.State != transfer.StatePaused {
		t.Fatalf("paused state = %s", got.State)
	}
	if pauses, _, _, _ := fake.counters(); pauses != 1 {
		t.Fatalf("qB pause calls = %d", pauses)
	}

	if err := svc.Resume(ctx, e.principal, tr.ID); err != nil {
		t.Fatalf("Resume: %v", err)
	}
	got, err = e.db.TransferByID(ctx, sc, tr.ID)
	if err != nil {
		t.Fatalf("load resumed: %v", err)
	}
	if got.State != transfer.StatePending {
		t.Fatalf("resumed state = %s", got.State)
	}
	if _, resumes, _, _ := fake.counters(); resumes != 1 {
		t.Fatalf("qB resume calls = %d", resumes)
	}

	savePath := svc.qbSavePath(tr)
	if err := os.MkdirAll(savePath, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := svc.Delete(ctx, e.principal, tr.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, _, deletes, deleteArg := fake.counters(); deletes != 1 || deleteArg != testHash+"|true" {
		t.Fatalf("qB delete calls = %d arg = %q", deletes, deleteArg)
	}
	if _, err := e.db.TransferByID(ctx, sc, tr.ID); err == nil {
		t.Fatal("transfer row survived deletion")
	}
	if _, err := os.Stat(savePath); !os.IsNotExist(err) {
		t.Fatalf("save path survived deletion: %v", err)
	}
}

func TestQBCreateUnavailableWhenDisabled(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	if e.svc.QBEnabled() {
		t.Fatal("QBEnabled = true without configuration")
	}
	_, err := e.svc.CreateQB(ctx, e.principal, e.root, "x", qbMagnet(testHash, ""))
	appErr, ok := apperr.From(err)
	if !ok {
		t.Fatalf("err = %v, want an application error", err)
	}
	if appErr.Code != apperr.CodeQBTUnavailable || appErr.HTTPStatus != http.StatusServiceUnavailable {
		t.Fatalf("err = %s/%d", appErr.Code, appErr.HTTPStatus)
	}
}

func TestQBCreateRejectsInvalidMagnet(t *testing.T) {
	e, _, svc := newQBSvc(t)
	ctx := context.Background()
	cases := map[string]string{
		"empty":        "",
		"plain url":    "https://example.com/x.torrent",
		"wrong urn":    "magnet:?xt=urn:sha1:0123456789abcdef0123456789abcdef01234567",
		"missing xt":   "magnet:?dn=demo",
		"short hex":    "magnet:?xt=urn:btih:abc",
		"bad hex":      "magnet:?xt=urn:btih:zzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzz",
		"too long":     "magnet:?xt=urn:btih:" + testHash + "&dn=" + strings.Repeat("a", 2100),
		"bad base32":   "magnet:?xt=urn:btih:0123456789abcdefghijklmnopqrstuv",
		"truncated dn": "magnet:?xt=urn:btih:" + testHash + "&dn=%zz",
	}
	for name, magnet := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := svc.CreateQB(ctx, e.principal, e.root, "x", magnet)
			appErr, ok := apperr.From(err)
			if !ok {
				t.Fatalf("err = %v, want an application error", err)
			}
			if appErr.Code != apperr.CodeBadRequest || appErr.HTTPStatus != http.StatusBadRequest {
				t.Fatalf("err = %s/%d", appErr.Code, appErr.HTTPStatus)
			}
			if appErr.Reason != apperr.ReasonInvalidMagnet {
				t.Fatalf("reason = %q, want %q", appErr.Reason, apperr.ReasonInvalidMagnet)
			}
		})
	}
}

func TestQBBase32MagnetIsAccepted(t *testing.T) {
	e, _, svc := newQBSvc(t)
	// 32 base32 chars decode to the same 20 bytes as testHash.
	base32Hash := "AERUKZ4JVPG66AJDIVTYTK6N54ASGRLH"
	tr, err := svc.CreateQB(context.Background(), e.principal, e.root, "b32", "magnet:?xt=urn:btih:"+base32Hash)
	if err != nil {
		t.Fatalf("CreateQB: %v", err)
	}
	if tr.QBHash != testHash {
		t.Fatalf("hash = %q, want %q", tr.QBHash, testHash)
	}
}

func TestQBTracksAtMostTwoJobs(t *testing.T) {
	e, fake, svc := newQBSvc(t)
	ctx := context.Background()
	for _, h := range []string{testHash, hashTwo, hashThree} {
		if _, err := svc.CreateQB(ctx, e.principal, e.root, "", qbMagnet(h, "")); err != nil {
			t.Fatalf("CreateQB(%s): %v", h, err)
		}
	}
	if n := svc.RunQB(ctx); n != 2 {
		t.Fatalf("first RunQB started %d, want 2", n)
	}
	svc.Wait()
	if got := len(fake.addForms()); got != 2 {
		t.Fatalf("add calls = %d, want 2", got)
	}

	list, _, err := svc.List(ctx, e.principal, "", 50, 0)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	pending := 0
	for _, t := range list {
		if t.State == transfer.StatePending {
			pending++
		}
	}
	if pending != 1 {
		t.Fatalf("pending jobs = %d, want 1 (third job not tracked yet)", pending)
	}

	// Push the polled rows' updated_at forward: the untracked job is now the
	// least recently polled and must be picked next.
	if _, err := e.db.ExecContext(ctx, `UPDATE transfers SET updated_at = updated_at + 100 WHERE state = 'running'`); err != nil {
		t.Fatalf("bump updated_at: %v", err)
	}
	if n := svc.RunQB(ctx); n != 2 {
		t.Fatalf("second RunQB started %d, want 2 (rotated job + one running)", n)
	}
	svc.Wait()
	if got := len(fake.addForms()); got != 3 {
		t.Fatalf("add calls after rotation = %d, want 3", got)
	}
}
