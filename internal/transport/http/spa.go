package httpx

import (
	"io"
	"io/fs"
	"net/http"
	"path"
	"strconv"
	"strings"

	"rapig/internal/platform/apperr"
)

// apiRouteMethods lists the registered API endpoints and their allowed
// methods. A wrong method on a known path must answer 405 (with Allow) rather
// than fall through to the unknown-endpoint 404, otherwise clients cannot tell
// a typo in the method from a typo in the path. Patterns use one {param}
// placeholder per path segment, matching the method mux patterns.
var apiRouteMethods = map[string]string{
	"/api/v1/auth/login":                               "POST",
	"/api/v1/auth/logout":                              "POST",
	"/api/v1/auth/me":                                  "GET",
	"/api/v1/auth/registration":                        "GET",
	"/api/v1/auth/register":                            "POST",
	"/api/v1/auth/password":                            "POST",
	"/api/v1/auth/deletion":                            "GET",
	"/api/v1/auth/deletion/request":                    "POST",
	"/api/v1/auth/deletion/cancel":                     "POST",
	"/api/v1/nodes":                                    "GET, POST",
	"/api/v1/nodes/search":                             "GET",
	"/api/v1/nodes/{id}":                               "DELETE, PATCH",
	"/api/v1/nodes/{id}/restore":                       "POST",
	"/api/v1/nodes/{id}/content":                       "GET",
	"/api/v1/nodes/{id}/copy":                          "POST",
	"/api/v1/trash":                                    "GET",
	"/api/v1/trash/empty":                              "POST",
	"/api/v1/uploads":                                  "POST",
	"/api/v1/uploads/{id}/parts/{part}":                "PUT",
	"/api/v1/uploads/{id}/complete":                    "POST",
	"/api/v1/uploads/{id}":                             "DELETE",
	"/api/v1/shares":                                   "GET, POST",
	"/api/v1/shares/{id}":                              "DELETE",
	"/api/v1/shares/{id}/restore":                      "POST",
	"/api/v1/transfers":                                "GET, POST",
	"/api/v1/transfers/qb/status":                      "GET",
	"/api/v1/transfers/torrent":                        "POST",
	"/api/v1/transfers/{id}/pause":                     "POST",
	"/api/v1/transfers/{id}/resume":                    "POST",
	"/api/v1/transfers/{id}":                           "DELETE",
	"/api/v1/admin/users":                              "GET, POST",
	"/api/v1/admin/users/{id}":                         "PATCH",
	"/api/v1/admin/users/{id}/reset-password":          "POST",
	"/api/v1/admin/users/{id}/restore":                 "POST",
	"/api/v1/admin/users/{id}/purge":                   "POST",
	"/api/v1/admin/audit":                              "GET",
	"/api/v1/admin/audit/actions":                      "GET",
	"/api/v1/admin/settings":                           "GET, PUT",
	"/api/v1/admin/storage":                            "GET, POST",
	"/api/v1/admin/storage/{id}":                       "PATCH, DELETE",
	"/api/v1/groups":                                   "GET, POST",
	"/api/v1/admin/groups":                             "GET, POST",
	"/api/v1/admin/groups/{id}":                        "PATCH, DELETE",
	"/api/v1/admin/groups/{id}/members":                "GET, POST",
	"/api/v1/admin/groups/{id}/members/{user_id}":      "DELETE",
	"/api/v1/storage/policies":                         "GET",
	"/api/v1/public/shares/{token}":                    "GET",
	"/api/v1/public/shares/{token}/verify":             "POST",
	"/api/v1/public/shares/{token}/nodes":              "GET",
	"/api/v1/public/shares/{token}/nodes/{id}/content": "GET",
}

// apiAllowedMethods matches a request path against the API route table. It is
// only consulted for paths the mux did not route, i.e. exactly the wrong-method
// case.
func apiAllowedMethods(p string) (string, bool) {
	if m, ok := apiRouteMethods[p]; ok {
		return m, true
	}
	reqSegs := splitPath(p)
	for pattern, methods := range apiRouteMethods {
		if patternMatches(pattern, reqSegs) {
			return methods, true
		}
	}
	return "", false
}

func splitPath(p string) []string {
	trimmed := strings.Trim(p, "/")
	if trimmed == "" {
		return nil
	}
	return strings.Split(trimmed, "/")
}

func patternMatches(pattern string, reqSegs []string) bool {
	if !strings.Contains(pattern, "{") {
		return false
	}
	patSegs := splitPath(pattern)
	if len(patSegs) != len(reqSegs) {
		return false
	}
	for i, seg := range patSegs {
		if strings.HasPrefix(seg, "{") && strings.HasSuffix(seg, "}") {
			if reqSegs[i] == "" {
				return false
			}
			continue
		}
		if seg != reqSegs[i] {
			return false
		}
	}
	return true
}

// knownSPARoutes are the client side routes the SPA actually implements. The
// shell is served for them with 200; any other page path still gets the shell
// (so client side navigation and refreshes keep working) but with a 404
// status, because a typo like /fiels should not look like a real page to
// crawlers, caches or uptime checks.
var knownSPARoutes = []string{"/", "/index.html", "/login", "/register", "/files", "/trash", "/settings", "/shares", "/transfers", "/downloads", "/search", "/groups", "/admin/users", "/admin/audit", "/admin/settings", "/admin/storage", "/admin/groups", "/cancel-deletion"}

func isKnownSPARoute(p string) bool {
	for _, route := range knownSPARoutes {
		if p == route {
			return true
		}
	}
	// One-segment client routes: /files/{id} (folder view), /s/{token} (share
	// placeholder) and /groups/{id} (group space). Deeper paths under them are
	// not real pages.
	for _, prefix := range []string{"/files/", "/s/", "/groups/"} {
		if rest, ok := strings.CutPrefix(p, prefix); ok {
			return rest != "" && !strings.Contains(rest, "/")
		}
	}
	return false
}

// handleFallback serves the built SPA and its static assets. Unknown API
// paths get a JSON 404; unknown page paths get index.html with a 404 status
// (client side 404 view); missing /assets/* files get a hard 404 instead of
// HTML so broken script URLs are visible in the network tab.
func (s *Server) handleFallback(w http.ResponseWriter, r *http.Request) {
	if isAPIPath(r.URL.Path) {
		if allowed, ok := apiAllowedMethods(r.URL.Path); ok {
			w.Header().Set("Allow", allowed)
			s.writeError(w, r, apperr.ErrMethodNotAllowed)
			return
		}
		s.writeError(w, r, apperr.ErrNotFound.WithReason(apperr.ReasonUnknownEndpoint))
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		s.writeError(w, r, apperr.ErrMethodNotAllowed)
		return
	}
	if !s.webAvailable {
		s.writeError(w, r, apperr.ErrFrontendNotBuilt)
		return
	}

	clean := path.Clean("/" + r.URL.Path)
	rel := strings.TrimPrefix(clean, "/")
	if rel != "" && rel != "index.html" {
		if s.serveStatic(w, r, rel) {
			return
		}
		if strings.HasPrefix(rel, "assets/") {
			s.writeError(w, r, apperr.ErrNotFound.WithReason(apperr.ReasonMissingAsset))
			return
		}
	}
	if isKnownSPARoute(clean) {
		s.serveIndex(w, r, http.StatusOK)
		return
	}
	s.serveIndex(w, r, http.StatusNotFound)
}

func isAPIPath(p string) bool {
	return p == "/api" || strings.HasPrefix(p, "/api/")
}

func (s *Server) serveStatic(w http.ResponseWriter, r *http.Request, rel string) bool {
	f, err := s.webFS.Open(rel)
	if err != nil {
		return false
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || info.IsDir() {
		return false
	}
	data, err := io.ReadAll(f)
	if err != nil {
		s.log.Warn("static asset read failed", "path", rel, "err", err)
		return false
	}
	setStaticHeaders(w, rel)
	w.Header().Set("Content-Type", contentTypeFor(rel))
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodHead {
		_, _ = w.Write(data)
	}
	return true
}

func (s *Server) serveIndex(w http.ResponseWriter, r *http.Request, status int) {
	data, err := fs.ReadFile(s.webFS, "index.html")
	if err != nil {
		s.writeError(w, r, apperr.ErrFrontendNotBuilt.WithCause(err))
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	w.WriteHeader(status)
	if r.Method != http.MethodHead {
		_, _ = w.Write(data)
	}
}

func setStaticHeaders(w http.ResponseWriter, rel string) {
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")
	w.Header().Set("X-Frame-Options", "DENY")
	if strings.HasPrefix(rel, "assets/") {
		// Vite emits content hashed filenames, safe to cache forever.
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	} else {
		w.Header().Set("Cache-Control", "no-cache")
	}
}

func contentTypeFor(rel string) string {
	switch strings.ToLower(path.Ext(rel)) {
	case ".html":
		return "text/html; charset=utf-8"
	case ".js", ".mjs":
		return "text/javascript; charset=utf-8"
	case ".css":
		return "text/css; charset=utf-8"
	case ".json", ".map":
		return "application/json; charset=utf-8"
	case ".svg":
		return "image/svg+xml"
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".gif":
		return "image/gif"
	case ".webp":
		return "image/webp"
	case ".ico":
		return "image/x-icon"
	case ".woff":
		return "font/woff"
	case ".woff2":
		return "font/woff2"
	case ".ttf":
		return "font/ttf"
	case ".txt":
		return "text/plain; charset=utf-8"
	default:
		return "application/octet-stream"
	}
}
