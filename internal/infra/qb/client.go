// Package qb is a minimal client for the qBittorrent WebUI API v2. It is
// deliberately limited to the calls Rapig needs for offline torrent
// downloads and uses only the standard library (net/http + cookie jar).
//
// Credentials live in memory only; they are never written to the database or
// logged by this package.
package qb

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"sync"
	"time"
)

// Sentinel errors callers can classify with errors.Is.
var (
	// ErrDisabled reports a client that was built with an empty base URL.
	ErrDisabled = errors.New("qb: client not configured")
	// ErrAuth reports rejected credentials or an unrecoverable 403.
	ErrAuth = errors.New("qb: authentication failed")
	// ErrRejected reports a request qBittorrent refused (body "Fails.").
	ErrRejected = errors.New("qb: request rejected")
	// ErrDuplicate reports an add request for a torrent qBittorrent already
	// has (HTTP 409 from /torrents/add).
	ErrDuplicate = errors.New("qb: duplicate torrent")
)

// qbUserAgent identifies Rapig to the WebUI; qBittorrent 4.x is picky about
// missing/broken user agents on some configurations.
const qbUserAgent = "rapig-qbclient/1.0"

// StatusError reports a non-200 HTTP response.
type StatusError struct {
	StatusCode int
	Body       string
}

func (e *StatusError) Error() string {
	if e.Body == "" {
		return fmt.Sprintf("qb: unexpected status %d", e.StatusCode)
	}
	return fmt.Sprintf("qb: unexpected status %d: %s", e.StatusCode, e.Body)
}

const (
	pathLogin   = "/api/v2/auth/login"
	pathVersion = "/api/v2/app/version"
	pathAdd     = "/api/v2/torrents/add"
	pathInfo    = "/api/v2/torrents/info"
	pathFiles   = "/api/v2/torrents/files"
	pathPause   = "/api/v2/torrents/pause"  // 4.x
	pathStop    = "/api/v2/torrents/stop"   // 5.x
	pathResume  = "/api/v2/torrents/resume" // 4.x
	pathStart   = "/api/v2/torrents/start"  // 5.x
	pathDelete  = "/api/v2/torrents/delete"

	// maxResponseBytes bounds every response body read.
	maxResponseBytes = 8 << 20
	// maxLoginBytes bounds the tiny login response.
	maxLoginBytes = 1 << 10
	// defaultTimeout bounds one request; metadata calls carry no body so a
	// short bound keeps the 2s poller responsive even when qB is wedged.
	defaultTimeout = 15 * time.Second
)

// Torrent is the subset of /api/v2/torrents/info Rapig consumes.
type Torrent struct {
	Hash        string  `json:"hash"`
	Name        string  `json:"name"`
	Size        int64   `json:"size"`
	Progress    float64 `json:"progress"`
	State       string  `json:"state"`
	Category    string  `json:"category"`
	SavePath    string  `json:"save_path"`
	ContentPath string  `json:"content_path"`
}

// TorrentFile is one entry of /api/v2/torrents/files. Name is relative to the
// torrent's save path (multi-file torrents include the torrent root
// directory); Priority 0 means the file was skipped in qBittorrent.
type TorrentFile struct {
	Index    int     `json:"index"`
	Name     string  `json:"name"`
	Size     int64   `json:"size"`
	Progress float64 `json:"progress"`
	Priority int     `json:"priority"`
}

// Client talks to one qBittorrent instance.
type Client struct {
	base     string
	username string
	password string
	http     *http.Client

	mu       sync.Mutex
	loggedIn bool
	loginMu  sync.Mutex
}

// New builds a client. An empty baseURL yields ErrDisabled so callers can
// treat "not configured" as a distinct state from a configuration error.
func New(baseURL, username, password string) (*Client, error) {
	raw := strings.TrimSpace(baseURL)
	if raw == "" {
		return nil, ErrDisabled
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("qb: invalid base URL %q", raw)
	}
	u.Path = strings.TrimRight(u.Path, "/")
	u.RawQuery = ""
	u.Fragment = ""
	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, fmt.Errorf("qb: cookie jar: %w", err)
	}
	return &Client{
		base:     strings.TrimRight(u.String(), "/"),
		username: username,
		password: password,
		http: &http.Client{
			Jar:     jar,
			Timeout: defaultTimeout,
		},
	}, nil
}

// EnsureLogin logs in only when no SID cookie is known to be valid.
func (c *Client) EnsureLogin(ctx context.Context) error {
	c.mu.Lock()
	ok := c.loggedIn
	c.mu.Unlock()
	if ok {
		return nil
	}
	return c.Login(ctx)
}

// Login authenticates with the WebUI. qBittorrent answers "Ok." on success
// and "Fails." (HTTP 200) on bad credentials; both are handled.
func (c *Client) Login(ctx context.Context) error {
	c.loginMu.Lock()
	defer c.loginMu.Unlock()
	if c.isLoggedIn() {
		return nil
	}
	form := url.Values{"username": {c.username}, "password": {c.password}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+pathLogin, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	c.setCommonHeaders(req)
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("qb: login: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxLoginBytes))
	if err != nil {
		return fmt.Errorf("qb: login: read response: %w", err)
	}
	text := strings.TrimSpace(string(body))
	switch {
	case resp.StatusCode == http.StatusNoContent:
		// qBittorrent 5.x answers 204 with an empty body and sets the SID
		// cookie; 4.x answered 200 "Ok.".
		c.setLoggedIn(true)
		return nil
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		c.setLoggedIn(false)
		return ErrAuth
	case resp.StatusCode != http.StatusOK:
		return &StatusError{StatusCode: resp.StatusCode, Body: truncate(text)}
	case text == "Fails.":
		c.setLoggedIn(false)
		return ErrAuth
	case text == "Ok.":
		c.setLoggedIn(true)
		return nil
	default:
		return &StatusError{StatusCode: resp.StatusCode, Body: truncate(text)}
	}
}

// Version returns the qBittorrent version string (for example "v5.0.1").
func (c *Client) Version(ctx context.Context) (string, error) {
	body, err := c.request(ctx, http.MethodGet, pathVersion, "", nil)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(body)), nil
}

// AddTorrent queues one or more magnet/torrent URLs. savePath and category
// may be empty. Old qBittorrent releases answer with an empty body, newer
// ones with "Ok."; both are accepted.
func (c *Client) AddTorrent(ctx context.Context, urls []string, savePath, category string, paused bool) error {
	if len(urls) == 0 {
		return fmt.Errorf("qb: add torrent: no urls")
	}
	form := url.Values{
		"urls":   {strings.Join(urls, "\n")},
		"paused": {boolString(paused)},
	}
	if savePath != "" {
		form.Set("savepath", savePath)
	}
	if category != "" {
		form.Set("category", category)
	}
	body, err := c.request(ctx, http.MethodPost, pathAdd, "", form)
	if err != nil {
		return err
	}
	return addTorrentResponse(body)
}

// AddTorrentFile uploads one .torrent file. It is separate from AddTorrent
// because qBittorrent expects a multipart/form-data body with the file in a
// field named "torrents"; the rest of the form (savepath, category, paused)
// mirrors a magnet add. Responses are interpreted by addTorrentResponse, so
// 4.x (empty/"Ok.") and 5.x (JSON) both work.
func (c *Client) AddTorrentFile(ctx context.Context, filename string, data []byte, savePath, category string, paused bool) error {
	if len(data) == 0 {
		return fmt.Errorf("qb: add torrent file: empty file")
	}
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	// qBittorrent reads the plain fields before the file part; keep that
	// order and surface write errors instead of dropping a field silently.
	for _, field := range [][2]string{{"savepath", savePath}, {"category", category}} {
		if field[1] == "" {
			continue
		}
		if err := mw.WriteField(field[0], field[1]); err != nil {
			return fmt.Errorf("qb: add torrent file: %w", err)
		}
	}
	if err := mw.WriteField("paused", boolString(paused)); err != nil {
		return fmt.Errorf("qb: add torrent file: %w", err)
	}
	part, err := mw.CreateFormFile("torrents", torrentFilename(filename))
	if err != nil {
		return fmt.Errorf("qb: add torrent file: %w", err)
	}
	if _, err := part.Write(data); err != nil {
		return fmt.Errorf("qb: add torrent file: %w", err)
	}
	if err := mw.Close(); err != nil {
		return fmt.Errorf("qb: add torrent file: %w", err)
	}
	body, err := c.do(ctx, http.MethodPost, pathAdd, "", mw.FormDataContentType(), buf.Bytes())
	if err != nil {
		// A 400/415 here means qBittorrent understood the request and refused
		// the torrent itself, so report it as a rejection instead of an
		// outage; every other failure stays whatever do returned.
		var se *StatusError
		if errors.As(err, &se) && (se.StatusCode == http.StatusBadRequest || se.StatusCode == http.StatusUnsupportedMediaType) {
			return fmt.Errorf("%w: %s", ErrRejected, se.Error())
		}
		// qBittorrent 5.x answers a duplicate torrent with 409 Conflict.
		if errors.As(err, &se) && se.StatusCode == http.StatusConflict {
			return ErrDuplicate
		}
		return err
	}
	return addTorrentResponse(body)
}

// addTorrentResponse interprets the /torrents/add answer: an empty body or
// "Ok." (4.x) and a JSON summary (5.x) all mean success unless the summary
// reports failures.
func addTorrentResponse(body []byte) error {
	text := strings.TrimSpace(string(body))
	if strings.HasPrefix(text, "{") {
		// qBittorrent 5.x answers with a JSON summary instead of "Ok."
		var summary struct {
			SuccessCount int `json:"success_count"`
			FailureCount int `json:"failure_count"`
		}
		if err := json.Unmarshal([]byte(text), &summary); err != nil {
			return fmt.Errorf("%w: %s", ErrRejected, truncate(text))
		}
		if summary.FailureCount > 0 {
			return fmt.Errorf("%w: %s", ErrRejected, truncate(text))
		}
		return nil
	}
	switch text {
	case "", "Ok.":
		return nil
	case "Fails.":
		return ErrRejected
	default:
		return fmt.Errorf("%w: %s", ErrRejected, truncate(text))
	}
}

// torrentFilename maps a client-supplied upload name to a safe multipart
// filename: bare base name, no control characters, never empty.
func torrentFilename(name string) string {
	if i := strings.LastIndexAny(name, `/\`); i >= 0 {
		name = name[i+1:]
	}
	var b strings.Builder
	for _, r := range name {
		if r < 0x20 || r == 0x7f {
			continue
		}
		b.WriteRune(r)
	}
	out := strings.TrimSpace(b.String())
	if out == "" || out == "." || out == ".." {
		return "file.torrent"
	}
	return out
}

// TorrentInfo returns metadata for hashes; an empty hashes slice asks for all
// torrents.
func (c *Client) TorrentInfo(ctx context.Context, hashes []string) ([]Torrent, error) {
	query := ""
	if len(hashes) > 0 {
		// Infohashes are hex/base32, so joining them unescaped is safe and
		// matches what the WebUI expects.
		query = "hashes=" + strings.Join(hashes, "|")
	}
	body, err := c.request(ctx, http.MethodGet, pathInfo, query, nil)
	if err != nil {
		return nil, err
	}
	if len(body) == 0 {
		return nil, nil
	}
	var out []Torrent
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("qb: torrents/info: decode response: %w", err)
	}
	return out, nil
}

// Files returns the file list of one torrent. qBittorrent 4.x and 5.x use the
// same endpoint and field names; skipped files report priority 0.
func (c *Client) Files(ctx context.Context, hash string) ([]TorrentFile, error) {
	if hash == "" {
		return nil, fmt.Errorf("qb: torrents/files: empty hash")
	}
	body, err := c.request(ctx, http.MethodGet, pathFiles, "hash="+url.QueryEscape(hash), nil)
	if err != nil {
		return nil, err
	}
	if len(body) == 0 {
		return nil, nil
	}
	var out []TorrentFile
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("qb: torrents/files: decode response: %w", err)
	}
	return out, nil
}

// Pause pauses the given torrents. 4.x uses /pause, 5.x uses /stop.
func (c *Client) Pause(ctx context.Context, hashes []string) error {
	err := c.torrentAction(ctx, pathPause, hashes, nil)
	if isNotFound(err) {
		return c.torrentAction(ctx, pathStop, hashes, nil)
	}
	return err
}

// Resume resumes the given torrents. 4.x uses /resume, 5.x uses /start.
func (c *Client) Resume(ctx context.Context, hashes []string) error {
	err := c.torrentAction(ctx, pathResume, hashes, nil)
	if isNotFound(err) {
		return c.torrentAction(ctx, pathStart, hashes, nil)
	}
	return err
}

// isNotFound reports whether err is a 404 from a qBittorrent endpoint that
// does not exist on the connected version.
func isNotFound(err error) bool {
	var se *StatusError
	return errors.As(err, &se) && se.StatusCode == http.StatusNotFound
}

// Delete removes the given torrents, optionally deleting their files.
func (c *Client) Delete(ctx context.Context, hashes []string, deleteFiles bool) error {
	return c.torrentAction(ctx, pathDelete, hashes, url.Values{"deleteFiles": {boolString(deleteFiles)}})
}

func (c *Client) torrentAction(ctx context.Context, path string, hashes []string, extra url.Values) error {
	if len(hashes) == 0 {
		return fmt.Errorf("qb: %s: no hashes", path)
	}
	form := url.Values{"hashes": {strings.Join(hashes, "|")}}
	for k, vs := range extra {
		form[k] = vs
	}
	_, err := c.request(ctx, http.MethodPost, path, "", form)
	return err
}

// request performs an authenticated form call.
func (c *Client) request(ctx context.Context, method, path, rawQuery string, form url.Values) ([]byte, error) {
	var body []byte
	contentType := ""
	if form != nil {
		body = []byte(form.Encode())
		contentType = "application/x-www-form-urlencoded"
	}
	return c.do(ctx, method, path, rawQuery, contentType, body)
}

// do performs an authenticated API call with an optional pre-encoded body,
// retrying once through a fresh login when qBittorrent answers 403 (expired
// SID cookie). The body is buffered by the caller so the retry can resend it.
func (c *Client) do(ctx context.Context, method, path, rawQuery, contentType string, body []byte) ([]byte, error) {
	u := c.base + path
	if rawQuery != "" {
		u += "?" + rawQuery
	}
	for attempt := 0; ; attempt++ {
		if err := c.EnsureLogin(ctx); err != nil {
			return nil, err
		}
		var rdr io.Reader
		if body != nil {
			rdr = bytes.NewReader(body)
		}
		req, err := http.NewRequestWithContext(ctx, method, u, rdr)
		if err != nil {
			return nil, err
		}
		if contentType != "" {
			req.Header.Set("Content-Type", contentType)
		}
		c.setCommonHeaders(req)
		resp, err := c.http.Do(req)
		if err != nil {
			return nil, fmt.Errorf("qb: %s %s: %w", method, path, err)
		}
		data, readErr := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
		closeErr := resp.Body.Close()
		if readErr != nil {
			return nil, fmt.Errorf("qb: %s %s: read response: %w", method, path, readErr)
		}
		if closeErr != nil {
			return nil, fmt.Errorf("qb: %s %s: close response: %w", method, path, closeErr)
		}
		if len(data) > maxResponseBytes {
			return nil, fmt.Errorf("qb: %s %s: response exceeds %d bytes", method, path, maxResponseBytes)
		}
		if resp.StatusCode == http.StatusForbidden {
			if attempt == 0 {
				c.setLoggedIn(false)
				continue
			}
			return nil, ErrAuth
		}
		if resp.StatusCode == http.StatusNoContent {
			// 5.x uses 204 for several mutating calls (login included).
			return nil, nil
		}
		if resp.StatusCode != http.StatusOK {
			return nil, &StatusError{StatusCode: resp.StatusCode, Body: truncate(string(data))}
		}
		return data, nil
	}
}

// setCommonHeaders adds headers required by qBittorrent's CSRF/host
// protection: WebUI 4.2+ rejects POSTs whose Origin does not match the
// WebUI origin, and the Referer keeps older 4.x builds happy.
func (c *Client) setCommonHeaders(req *http.Request) {
	req.Header.Set("Referer", c.base+"/")
	req.Header.Set("Origin", c.base)
	req.Header.Set("User-Agent", qbUserAgent)
}

func (c *Client) isLoggedIn() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.loggedIn
}

func (c *Client) setLoggedIn(v bool) {
	c.mu.Lock()
	c.loggedIn = v
	c.mu.Unlock()
}

func boolString(v bool) string {
	if v {
		return "true"
	}
	return "false"
}

func truncate(s string) string {
	const max = 200
	s = strings.TrimSpace(s)
	if len(s) > max {
		return s[:max]
	}
	return s
}
