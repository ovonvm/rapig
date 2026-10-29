package httpx

import (
	"net/http"
	"os"
	"path/filepath"

	idsvc "rapig/internal/app/identity"
	"rapig/internal/domain/access"
)

// handleRegistrationStatus reports whether self registration is open. It needs
// no session: the login and register pages call it before signing in.
func (s *Server) handleRegistrationStatus(w http.ResponseWriter, r *http.Request) {
	enabled, err := s.identity.RegistrationEnabled(r.Context())
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"enabled": enabled})
}

// handleRegister creates an account while self registration is enabled. It
// deliberately does not start a session: the client logs in afterwards
// (docs/15-m5-plan.md §11).
func (s *Server) handleRegister(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Username string `json:"username"`
		Email    string `json:"email"`
		Phone    string `json:"phone"`
		Password string `json:"password"`
	}
	if err := decodeJSON(w, r, &req); err != nil {
		s.writeError(w, r, err)
		return
	}
	user, err := s.identity.Register(r.Context(), idsvc.RegisterRequest{
		Username:  req.Username,
		Email:     req.Email,
		Phone:     req.Phone,
		Password:  req.Password,
		IP:        clientIP(r),
		UserAgent: truncateText(r.UserAgent(), 512),
		RequestID: requestID(r),
	})
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	s.writeJSON(w, http.StatusCreated, map[string]any{"user": toUserJSON(user)})
}

// handleChangePassword verifies the current password and stores a new one. All
// other sessions of the account are revoked; the session that performed the
// change stays valid. There is deliberately no session management API.
func (s *Server) handleChangePassword(w http.ResponseWriter, r *http.Request) {
	p, err := requireUser(r)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	if err := s.authz.Require(p, access.ActionWrite, access.SystemResource()); err != nil {
		s.writeError(w, r, err)
		return
	}
	var req struct {
		CurrentPassword string `json:"current_password"`
		NewPassword     string `json:"new_password"`
	}
	if err := decodeJSON(w, r, &req); err != nil {
		s.writeError(w, r, err)
		return
	}
	meta := idsvc.LoginRequest{
		IP:        clientIP(r),
		UserAgent: truncateText(r.UserAgent(), 512),
		RequestID: requestID(r),
	}
	if err := s.identity.ChangePassword(r.Context(), p.UserID, sessionIDFrom(r),
		req.CurrentPassword, req.NewPassword, meta); err != nil {
		s.writeError(w, r, err)
		return
	}
	// The one-time bootstrap password file is useless once the password has
	// changed; remove it so installs do not leave a stale secret on disk.
	if err := os.Remove(filepath.Join(s.cfg.DataDir, "bootstrap", "initial-admin.txt")); err == nil {
		s.log.Info("removed initial admin password file after password change")
	}
	w.WriteHeader(http.StatusNoContent)
}
