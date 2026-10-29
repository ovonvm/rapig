package httpx

import (
	"context"
	"crypto/subtle"
	"net/http"
	"net/url"
	"runtime/debug"
	"strings"
	"time"

	"rapig/internal/domain/access"
	"rapig/internal/domain/identity"
	"rapig/internal/platform/apperr"
	"rapig/internal/platform/config"
	"rapig/internal/platform/idgen"
)

type ctxKey int

const (
	ctxKeyRequestID ctxKey = iota
	ctxKeyPrincipal
	ctxKeyUser
	ctxKeyCSRFHash
	ctxKeySessionID
)

const (
	sessionCookieName = "rapig_session"
	csrfCookieName    = "rapig_csrf"
)

func withRequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := sanitizeRequestID(r.Header.Get("X-Request-ID"))
		if id == "" {
			id = idgen.RequestID()
		}
		ctx := context.WithValue(r.Context(), ctxKeyRequestID, id)
		w.Header().Set("X-Request-ID", id)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func sanitizeRequestID(id string) string {
	if len(id) == 0 || len(id) > 64 {
		return ""
	}
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
		default:
			return ""
		}
	}
	return id
}

func requestID(r *http.Request) string {
	if v, ok := r.Context().Value(ctxKeyRequestID).(string); ok {
		return v
	}
	return ""
}

type statusRecorder struct {
	http.ResponseWriter
	status int
	bytes  int
}

func (r *statusRecorder) WriteHeader(code int) {
	if r.status == 0 {
		r.status = code
	}
	r.ResponseWriter.WriteHeader(code)
}

func (r *statusRecorder) Write(b []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	n, err := r.ResponseWriter.Write(b)
	r.bytes += n
	return n, err
}

func withLogging(log interface {
	Info(msg string, args ...any)
	Warn(msg string, args ...any)
}, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w}
		next.ServeHTTP(rec, r)
		status := rec.status
		if status == 0 {
			status = http.StatusOK
		}
		args := []any{
			"method", r.Method,
			"path", r.URL.Path,
			"status", status,
			"bytes", rec.bytes,
			"duration_ms", time.Since(start).Milliseconds(),
			"request_id", requestID(r),
			"remote", r.RemoteAddr,
		}
		if status >= 500 {
			log.Warn("http request", args...)
		} else {
			log.Info("http request", args...)
		}
	})
}

func withRecover(log interface {
	Error(msg string, args ...any)
}, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				log.Error("panic serving request",
					"err", rec,
					"path", r.URL.Path,
					"request_id", requestID(r),
					"stack", string(debug.Stack()),
				)
				writePlainError(w, http.StatusInternalServerError, "服务器内部错误")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// withAuthn resolves the session cookie into a principal and stores it in the
// request context. Invalid sessions become anonymous; protected handlers then
// answer 401.
func (s *Server) withAuthn(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := sessionToken(r)
		if token == "" {
			next.ServeHTTP(w, r)
			return
		}
		user, p, sess, err := s.identity.Authenticate(r.Context(), token)
		if err != nil {
			if apperr.CodeOf(err) == apperr.CodeUnauthenticated {
				clearAuthCookies(w)
				next.ServeHTTP(w, r)
				return
			}
			s.log.Error("authn: session lookup failed", "err", err, "request_id", requestID(r))
			s.writeError(w, r, apperr.ErrInternal.WithCause(err))
			return
		}
		ctx := r.Context()
		ctx = context.WithValue(ctx, ctxKeyPrincipal, p)
		ctx = context.WithValue(ctx, ctxKeyUser, user)
		ctx = context.WithValue(ctx, ctxKeyCSRFHash, sess.CSRFHash)
		ctx = context.WithValue(ctx, ctxKeySessionID, sess.ID)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// withCSRF enforces the double submit cookie scheme on mutating /api requests.
// Safe requests get a CSRF cookie when they do not have one yet, which is what
// lets the SPA (and the Vite dev server) obtain the token before POSTing.
func (s *Server) withCSRF(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isSafeMethod(r.Method) {
			s.ensureCSRFCookie(w, r)
			next.ServeHTTP(w, r)
			return
		}
		if !strings.HasPrefix(r.URL.Path, "/api/") {
			next.ServeHTTP(w, r)
			return
		}
		cookie, err := r.Cookie(csrfCookieName)
		header := r.Header.Get("X-CSRF-Token")
		if err != nil || cookie.Value == "" || header == "" ||
			subtle.ConstantTimeCompare([]byte(cookie.Value), []byte(header)) != 1 {
			s.writeError(w, r, apperr.ErrCSRF)
			return
		}
		if !s.originAllowed(r) {
			s.writeError(w, r, apperr.ErrCSRF.WithReason(apperr.ReasonOriginMismatch))
			return
		}
		// Bind the token to the session when authenticated so a token planted
		// in another browser cannot be replayed against this session.
		if sessionCSRF, ok := r.Context().Value(ctxKeyCSRFHash).(string); ok && sessionCSRF != "" {
			if subtle.ConstantTimeCompare([]byte(idgen.SHA256Hex(header)), []byte(sessionCSRF)) != 1 {
				s.writeError(w, r, apperr.ErrCSRF)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func isSafeMethod(method string) bool {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return true
	default:
		return false
	}
}

// originAllowed implements the M1 reverse-proxy contract: compare only the
// host (scheme is ignored, because a TLS terminator always changes it), and
// treat RAPIG_ALLOW_ORIGINS entries as host allow-list entries. Both Origin
// and Referer may be absent; the double-submit token is then the only check.
func (s *Server) originAllowed(r *http.Request) bool {
	origin := strings.TrimSpace(r.Header.Get("Origin"))
	if origin == "" {
		origin = strings.TrimSpace(r.Header.Get("Referer"))
	}
	if origin == "" {
		return false
	}
	host := hostOf(origin)
	if host == "" {
		return false
	}
	if strings.EqualFold(host, r.Host) {
		return true
	}
	for _, allowed := range s.cfg.AllowOrigins {
		if strings.EqualFold(hostOf(allowed), host) {
			return true
		}
	}
	return false
}

// hostOf extracts the host (with port) from an origin or absolute URL,
// falling back to the raw string for bare host entries.
func hostOf(raw string) string {
	raw = strings.TrimSpace(strings.TrimRight(raw, "/"))
	if raw == "" {
		return ""
	}
	if u, err := url.Parse(raw); err == nil && u.Host != "" {
		return u.Host
	}
	return raw
}

// cookieSecure resolves the Secure flag for auth cookies. auto follows the
// request TLS state; always is for a TLS terminating reverse proxy; never is
// for plain HTTP development. X-Forwarded-Proto is deliberately ignored
// (docs/09 §2).
func (s *Server) cookieSecure(r *http.Request) bool {
	switch strings.ToLower(s.cfg.CookieSecure) {
	case config.CookieSecureAlways:
		return true
	case config.CookieSecureNever:
		return false
	default:
		return r.TLS != nil
	}
}

func sessionToken(r *http.Request) string {
	c, err := r.Cookie(sessionCookieName)
	if err != nil {
		return ""
	}
	return c.Value
}

func (s *Server) setLoginCookies(w http.ResponseWriter, r *http.Request, sessionToken, csrfToken string, expiresAt time.Time) {
	secure := s.cookieSecure(r)
	maxAge := int(time.Until(expiresAt).Seconds())
	if maxAge < 0 {
		maxAge = 0
	}
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    sessionToken,
		Path:     "/",
		MaxAge:   maxAge,
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
	})
	http.SetCookie(w, &http.Cookie{
		Name:     csrfCookieName,
		Value:    csrfToken,
		Path:     "/",
		MaxAge:   maxAge,
		HttpOnly: false, // the SPA must read this to set the header
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
	})
}

func (s *Server) ensureCSRFCookie(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(csrfCookieName); err == nil && c.Value != "" {
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     csrfCookieName,
		Value:    idgen.MustToken(32),
		Path:     "/",
		MaxAge:   int(identity.SessionTTL.Seconds()),
		HttpOnly: false,
		Secure:   s.cookieSecure(r),
		SameSite: http.SameSiteLaxMode,
	})
}

func clearAuthCookies(w http.ResponseWriter) {
	for _, name := range []string{sessionCookieName, csrfCookieName} {
		http.SetCookie(w, &http.Cookie{
			Name:     name,
			Value:    "",
			Path:     "/",
			MaxAge:   -1,
			HttpOnly: name == sessionCookieName,
			SameSite: http.SameSiteLaxMode,
		})
	}
}

func principalFrom(r *http.Request) (access.Principal, bool) {
	p, ok := r.Context().Value(ctxKeyPrincipal).(access.Principal)
	return p, ok
}

func sessionIDFrom(r *http.Request) string {
	id, _ := r.Context().Value(ctxKeySessionID).(string)
	return id
}

func userFrom(r *http.Request) (identity.User, bool) {
	u, ok := r.Context().Value(ctxKeyUser).(identity.User)
	return u, ok
}
