package qb

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
)

const (
	testUser = "admin"
	testPass = "secret"
)

type fakeQB struct {
	mu sync.Mutex

	sid        string
	loginCalls int
	expireOnce bool
	expired    bool

	torrents map[string]Torrent

	versionStatus int

	addCalls    int
	addForm     url.Values
	rejectAdd   bool
	pauseCalls  int
	resumeCalls int
	deleteCalls int
	deleteFiles string
	lastHashes  map[string]string
	lastQuery   string
}

func newFakeQB(t *testing.T) (*fakeQB, *httptest.Server) {
	t.Helper()
	f := &fakeQB{
		sid:        "sid-initial",
		torrents:   map[string]Torrent{},
		lastHashes: map[string]string{},
	}
	srv := httptest.NewServer(http.HandlerFunc(f.handle))
	t.Cleanup(srv.Close)
	return f, srv
}

func (f *fakeQB) handle(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if r.URL.Path == pathLogin {
		_ = r.ParseForm()
		f.loginCalls++
		if r.PostForm.Get("username") != testUser || r.PostForm.Get("password") != testPass {
			_, _ = w.Write([]byte("Fails."))
			return
		}
		f.sid = fmt.Sprintf("sid-%d", f.loginCalls)
		http.SetCookie(w, &http.Cookie{Name: "SID", Value: f.sid, Path: "/"})
		_, _ = w.Write([]byte("Ok."))
		return
	}
	if r.URL.Path == pathVersion && f.versionStatus != 0 && f.versionStatus != http.StatusOK {
		w.WriteHeader(f.versionStatus)
		return
	}
	cookie, err := r.Cookie("SID")
	if err != nil || cookie.Value != f.sid {
		w.WriteHeader(http.StatusForbidden)
		return
	}

	switch r.URL.Path {
	case pathVersion:
		_, _ = w.Write([]byte("v5.0.1"))
	case pathAdd:
		_ = r.ParseForm()
		f.addCalls++
		f.addForm = r.PostForm
		if f.rejectAdd {
			_, _ = w.Write([]byte("Fails."))
			return
		}
		for _, raw := range strings.Split(r.PostForm.Get("urls"), "\n") {
			raw = strings.TrimSpace(raw)
			if raw == "" {
				continue
			}
			h := strings.TrimPrefix(raw[strings.Index(raw, "btih:")+len("btih:"):], "urn:btih:")
			h = strings.ToLower(strings.TrimSpace(h))
			f.torrents[h] = Torrent{
				Hash:        h,
				Name:        "fake",
				State:       "downloading",
				Category:    r.PostForm.Get("category"),
				SavePath:    r.PostForm.Get("savepath"),
				ContentPath: r.PostForm.Get("savepath"),
			}
		}
		// Old qBittorrent releases return an empty body.
	case pathInfo:
		f.lastQuery = r.URL.RawQuery
		if f.expireOnce && !f.expired {
			f.expired = true
			f.sid = "sid-rotated"
			w.WriteHeader(http.StatusForbidden)
			return
		}
		filter := r.URL.Query().Get("hashes")
		out := make([]Torrent, 0, len(f.torrents))
		for h, tr := range f.torrents {
			if filter != "" && !containsHash(filter, h) {
				continue
			}
			out = append(out, tr)
		}
		_ = json.NewEncoder(w).Encode(out)
	case pathPause, pathResume, pathDelete:
		_ = r.ParseForm()
		hashes := r.PostForm.Get("hashes")
		switch r.URL.Path {
		case pathPause:
			f.pauseCalls++
		case pathResume:
			f.resumeCalls++
		case pathDelete:
			f.deleteCalls++
			f.deleteFiles = r.PostForm.Get("deleteFiles")
			for _, h := range strings.Split(hashes, "|") {
				delete(f.torrents, h)
			}
		}
		f.lastHashes[r.URL.Path] = hashes
		w.WriteHeader(http.StatusOK)
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func containsHash(filter, hash string) bool {
	for _, part := range strings.Split(filter, "|") {
		if part == hash {
			return true
		}
	}
	return false
}

func newTestClient(t *testing.T, srv *httptest.Server) *Client {
	t.Helper()
	c, err := New(srv.URL, testUser, testPass)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c
}

func TestNewRejectsBadBaseURL(t *testing.T) {
	if _, err := New("", testUser, testPass); !errors.Is(err, ErrDisabled) {
		t.Fatalf("empty URL err = %v, want ErrDisabled", err)
	}
	for _, raw := range []string{"ftp://example.com", "not-a-url", "http://"} {
		if _, err := New(raw, testUser, testPass); err == nil {
			t.Fatalf("%q: expected error", raw)
		}
	}
}

func TestLoginAndVersion(t *testing.T) {
	fake, srv := newFakeQB(t)
	c := newTestClient(t, srv)

	ctx := context.Background()
	if err := c.Login(ctx); err != nil {
		t.Fatalf("Login: %v", err)
	}
	v, err := c.Version(ctx)
	if err != nil {
		t.Fatalf("Version: %v", err)
	}
	if v != "v5.0.1" {
		t.Fatalf("version = %q", v)
	}
	if fake.loginCalls != 1 {
		t.Fatalf("login calls = %d, want 1", fake.loginCalls)
	}
	// The session is reused: a second call must not log in again.
	if _, err := c.Version(ctx); err != nil {
		t.Fatalf("Version second: %v", err)
	}
	if fake.loginCalls != 1 {
		t.Fatalf("login calls = %d, want 1 after second call", fake.loginCalls)
	}
}

func TestLoginFails(t *testing.T) {
	_, srv := newFakeQB(t)
	c, err := New(srv.URL, testUser, "wrong")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := c.Login(context.Background()); !errors.Is(err, ErrAuth) {
		t.Fatalf("err = %v, want ErrAuth", err)
	}
}

func TestAddTorrentSendsForm(t *testing.T) {
	fake, srv := newFakeQB(t)
	c := newTestClient(t, srv)

	magnet := "magnet:?xt=urn:btih:0123456789abcdef0123456789abcdef01234567&dn=file.iso"
	if err := c.AddTorrent(context.Background(), []string{magnet}, "/tmp/qb/1/task", "rapig-1", false); err != nil {
		t.Fatalf("AddTorrent: %v", err)
	}
	if fake.addForm.Get("urls") != magnet {
		t.Fatalf("urls = %q", fake.addForm.Get("urls"))
	}
	if fake.addForm.Get("savepath") != "/tmp/qb/1/task" {
		t.Fatalf("savepath = %q", fake.addForm.Get("savepath"))
	}
	if fake.addForm.Get("category") != "rapig-1" {
		t.Fatalf("category = %q", fake.addForm.Get("category"))
	}
	if fake.addForm.Get("paused") != "false" {
		t.Fatalf("paused = %q", fake.addForm.Get("paused"))
	}
}

func TestAddTorrentRejected(t *testing.T) {
	fake, srv := newFakeQB(t)
	fake.rejectAdd = true
	c := newTestClient(t, srv)
	err := c.AddTorrent(context.Background(), []string{"magnet:?xt=urn:btih:0123456789abcdef0123456789abcdef01234567"}, "", "", false)
	if !errors.Is(err, ErrRejected) {
		t.Fatalf("err = %v, want ErrRejected", err)
	}
}

func TestTorrentInfoAndActions(t *testing.T) {
	fake, srv := newFakeQB(t)
	c := newTestClient(t, srv)
	ctx := context.Background()

	hashes := []string{
		"0123456789abcdef0123456789abcdef01234567",
		"89abcdef0123456789abcdef0123456789abcdef",
	}
	for _, h := range hashes {
		if err := c.AddTorrent(ctx, []string{"magnet:?xt=urn:btih:" + h}, "/data/tmp", "rapig-7", false); err != nil {
			t.Fatalf("AddTorrent: %v", err)
		}
	}
	infos, err := c.TorrentInfo(ctx, hashes[:1])
	if err != nil {
		t.Fatalf("TorrentInfo: %v", err)
	}
	if len(infos) != 1 || infos[0].Hash != hashes[0] {
		t.Fatalf("infos = %+v", infos)
	}
	if infos[0].SavePath != "/data/tmp" || infos[0].Category != "rapig-7" {
		t.Fatalf("info = %+v", infos[0])
	}
	if fake.lastQuery != "hashes="+hashes[0] {
		t.Fatalf("single-hash query = %q", fake.lastQuery)
	}
	infos, err = c.TorrentInfo(ctx, hashes)
	if err != nil {
		t.Fatalf("TorrentInfo(all): %v", err)
	}
	if len(infos) != 2 {
		t.Fatalf("infos(all) = %+v", infos)
	}
	if fake.lastQuery != "hashes="+hashes[0]+"|"+hashes[1] {
		t.Fatalf("hashes query not joined with |: %q", fake.lastQuery)
	}

	if err := c.Pause(ctx, hashes); err != nil {
		t.Fatalf("Pause: %v", err)
	}
	if err := c.Resume(ctx, hashes); err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if err := c.Delete(ctx, hashes, true); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if fake.pauseCalls != 1 || fake.resumeCalls != 1 || fake.deleteCalls != 1 {
		t.Fatalf("calls pause=%d resume=%d delete=%d", fake.pauseCalls, fake.resumeCalls, fake.deleteCalls)
	}
	if fake.lastHashes[pathPause] != hashes[0]+"|"+hashes[1] {
		t.Fatalf("pause hashes = %q", fake.lastHashes[pathPause])
	}
	if fake.deleteFiles != "true" {
		t.Fatalf("deleteFiles = %q", fake.deleteFiles)
	}
}

func TestReloginAfterForbidden(t *testing.T) {
	fake, srv := newFakeQB(t)
	c := newTestClient(t, srv)
	ctx := context.Background()
	if err := c.Login(ctx); err != nil {
		t.Fatalf("Login: %v", err)
	}
	fake.mu.Lock()
	fake.expireOnce = true
	fake.mu.Unlock()

	infos, err := c.TorrentInfo(ctx, nil)
	if err != nil {
		t.Fatalf("TorrentInfo after 403: %v", err)
	}
	if len(infos) != 0 {
		t.Fatalf("infos = %+v", infos)
	}
	if fake.loginCalls != 2 {
		t.Fatalf("login calls = %d, want 2 (re-login after 403)", fake.loginCalls)
	}
}

func TestVersionStatusError(t *testing.T) {
	fake, srv := newFakeQB(t)
	fake.versionStatus = http.StatusInternalServerError
	c := newTestClient(t, srv)
	err := c.EnsureLogin(context.Background())
	if err == nil {
		// Login itself is fine; the error must come from Version.
		_, err = c.Version(context.Background())
	}
	var se *StatusError
	if !errors.As(err, &se) || se.StatusCode != http.StatusInternalServerError {
		t.Fatalf("err = %v, want StatusError 500", err)
	}
}

// TestLoginAccepts204FromQBittorrent5 verifies the 5.x login shape: HTTP 204
// with an empty body and the SID cookie (4.x answered 200 "Ok.").
func TestLoginAccepts204FromQBittorrent5(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case pathLogin:
			if err := r.ParseForm(); err != nil {
				t.Errorf("parse form: %v", err)
			}
			if r.FormValue("username") != testUser || r.FormValue("password") != testPass {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			http.SetCookie(w, &http.Cookie{Name: "QBT_SID_8080", Value: "v5", Path: "/"})
			w.WriteHeader(http.StatusNoContent)
		case pathVersion:
			if _, err := r.Cookie("QBT_SID_8080"); err != nil {
				w.WriteHeader(http.StatusForbidden)
				return
			}
			_, _ = w.Write([]byte("v5.2.3.10"))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	c, err := New(srv.URL, testUser, testPass)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := c.Login(context.Background()); err != nil {
		t.Fatalf("Login after 204: %v", err)
	}
	v, err := c.Version(context.Background())
	if err != nil {
		t.Fatalf("Version: %v", err)
	}
	if v != "v5.2.3.10" {
		t.Fatalf("version = %q, want v5.2.3.10", v)
	}

	bad, err := New(srv.URL, testUser, "wrong")
	if err != nil {
		t.Fatalf("New bad: %v", err)
	}
	if err := bad.Login(context.Background()); !errors.Is(err, ErrAuth) {
		t.Fatalf("bad credentials: err = %v, want ErrAuth", err)
	}
}

// TestAddTorrentAcceptsQBittorrent5JSON covers the 5.x add response shape:
// HTTP 200 with a JSON summary instead of 4.x's "Ok.".
func TestAddTorrentAcceptsQBittorrent5JSON(t *testing.T) {
	mode := "ok"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case pathLogin:
			w.WriteHeader(http.StatusNoContent)
		case pathAdd:
			if mode == "ok" {
				_, _ = w.Write([]byte(`{"added_torrent_ids":["abc"],"failure_count":0,"pending_count":0,"success_count":1}`))
				return
			}
			_, _ = w.Write([]byte(`{"added_torrent_ids":[],"failure_count":1,"pending_count":0,"success_count":0}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	c, err := New(srv.URL, testUser, testPass)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := c.AddTorrent(context.Background(), []string{"magnet:?xt=urn:btih:abc"}, "/tmp/x", "rapig-1", false); err != nil {
		t.Fatalf("AddTorrent JSON success: %v", err)
	}
	mode = "fail"
	if err := c.AddTorrent(context.Background(), []string{"magnet:?xt=urn:btih:abc"}, "/tmp/x", "rapig-1", false); !errors.Is(err, ErrRejected) {
		t.Fatalf("AddTorrent JSON failure: err = %v, want ErrRejected", err)
	}
}

func TestPauseFallsBackToStopOn404(t *testing.T) {
	var stopCalled bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case pathLogin:
			w.WriteHeader(http.StatusNoContent)
		case pathPause:
			w.WriteHeader(http.StatusNotFound)
		case pathStop:
			stopCalled = true
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	c := newTestClient(t, srv)
	if err := c.Pause(context.Background(), []string{"abc"}); err != nil {
		t.Fatalf("Pause fallback: %v", err)
	}
	if !stopCalled {
		t.Fatal("Pause did not fall back to /torrents/stop")
	}
}

func TestAddTorrentFileMaps409ToDuplicate(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case pathLogin:
			w.WriteHeader(http.StatusNoContent)
		case pathAdd:
			w.WriteHeader(http.StatusConflict)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	c := newTestClient(t, srv)
	err := c.AddTorrentFile(context.Background(), "x.torrent", []byte("d4:infod4:name1:xee"), "/tmp/x", "rapig-1", false)
	if !errors.Is(err, ErrDuplicate) {
		t.Fatalf("AddTorrentFile 409: err = %v, want ErrDuplicate", err)
	}
}

func TestAddTorrentFileMaps400ToRejected(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case pathLogin:
			w.WriteHeader(http.StatusNoContent)
		case pathAdd:
			w.WriteHeader(http.StatusBadRequest)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	c := newTestClient(t, srv)
	err := c.AddTorrentFile(context.Background(), "x.torrent", []byte("not-a-torrent"), "/tmp/x", "rapig-1", false)
	if !errors.Is(err, ErrRejected) {
		t.Fatalf("AddTorrentFile 400: err = %v, want ErrRejected", err)
	}
}

func TestResumeFallsBackToStartOn404(t *testing.T) {
	var startCalled bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case pathLogin:
			w.WriteHeader(http.StatusNoContent)
		case pathResume:
			w.WriteHeader(http.StatusNotFound)
		case pathStart:
			startCalled = true
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	c := newTestClient(t, srv)
	if err := c.Resume(context.Background(), []string{"abc"}); err != nil {
		t.Fatalf("Resume fallback: %v", err)
	}
	if !startCalled {
		t.Fatal("Resume did not fall back to /torrents/start")
	}
}
