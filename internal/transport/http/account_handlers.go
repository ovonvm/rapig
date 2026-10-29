package httpx

import (
	"net/http"
	"strconv"
	"time"

	"rapig/internal/domain/access"
	"rapig/internal/platform/apperr"
)

// deletionRequest is the re-authentication payload for a self-service deletion.
type deletionRequest struct {
	Password string `json:"password"`
}

// deletionCancelRequest carries the one-time token handed out by the request
// endpoint. The cancelling browser may well be signed out, so this endpoint is
// anonymous.
type deletionCancelRequest struct {
	Token string `json:"token"`
}

// deletionStatusJSON mirrors the account-level deletion fields the admin user
// list exposes, plus the site switch so the client can hide the action when the
// operator closed self-service deletion.
type deletionStatusJSON struct {
	Enabled    bool       `json:"enabled"`
	DeletedAt  *time.Time `json:"deleted_at"`
	PurgeAfter *time.Time `json:"purge_after"`
}

func (s *Server) handleDeletionStatus(w http.ResponseWriter, r *http.Request) {
	p, err := requireUser(r)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	status, err := s.account.StatusOf(r.Context(), p)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	s.writeJSON(w, http.StatusOK, deletionStatusJSON{
		Enabled:    status.Enabled,
		DeletedAt:  status.DeletedAt,
		PurgeAfter: status.PurgeAfter,
	})
}

func (s *Server) handleDeletionRequest(w http.ResponseWriter, r *http.Request) {
	p, err := requireUser(r)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	var req deletionRequest
	if err := decodeJSON(w, r, &req); err != nil {
		s.writeError(w, r, err)
		return
	}
	res, err := s.account.RequestDeletion(r.Context(), p, req.Password)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	// Every session was revoked inside the service; clearing the cookies makes
	// the browser agree instead of retrying a dead session.
	clearAuthCookies(w)
	s.writeJSON(w, http.StatusOK, map[string]any{
		"cancel_token": res.CancelToken,
		"purge_at":     res.PurgeAt.UTC().Format(time.RFC3339),
	})
}

func (s *Server) handleDeletionCancel(w http.ResponseWriter, r *http.Request) {
	var req deletionCancelRequest
	if err := decodeJSON(w, r, &req); err != nil {
		s.writeError(w, r, err)
		return
	}
	if err := s.account.CancelDeletion(r.Context(), req.Token); err != nil {
		s.writeError(w, r, err)
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"restored": true})
}

func (s *Server) handleAdminRestoreUser(w http.ResponseWriter, r *http.Request) {
	p, err := requireUser(r)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	if err := s.authz.Require(p, access.ActionAdminUsers, access.SystemResource()); err != nil {
		s.writeError(w, r, err)
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		s.writeError(w, r, apperr.ErrBadRequest.WithReason(apperr.ReasonInvalidID))
		return
	}
	user, err := s.account.RestoreUser(r.Context(), p, id)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"user": toAdminUserJSON(user)})
}

func (s *Server) handleAdminPurgeUser(w http.ResponseWriter, r *http.Request) {
	p, err := requireUser(r)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	if err := s.authz.Require(p, access.ActionAdminUsers, access.SystemResource()); err != nil {
		s.writeError(w, r, err)
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		s.writeError(w, r, apperr.ErrBadRequest.WithReason(apperr.ReasonInvalidID))
		return
	}
	if err := s.account.PurgeUser(r.Context(), p, id); err != nil {
		s.writeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
