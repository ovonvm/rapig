package httpx

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	adminsvc "rapig/internal/app/admin"
	"rapig/internal/domain/access"
	"rapig/internal/domain/identity"
	sqlstore "rapig/internal/infra/sql"
	"rapig/internal/platform/apperr"
)

// sqlstoreAuditFilter parses the audit page query string.
func sqlstoreAuditFilter(r *http.Request) sqlstore.AuditFilter {
	f := sqlstore.AuditFilter{
		Actor:  strings.TrimSpace(r.URL.Query().Get("actor")),
		Action: strings.TrimSpace(r.URL.Query().Get("action")),
	}
	if v := r.URL.Query().Get("from"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			f.From = time.Unix(n, 0).UTC()
		}
	}
	if v := r.URL.Query().Get("to"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			f.To = time.Unix(n, 0).UTC()
		}
	}
	return f
}

type adminUserJSON struct {
	ID          int64  `json:"id"`
	Username    string `json:"username"`
	DisplayName string `json:"display_name"`
	Role        string `json:"role"`
	Status      string `json:"status"`
	UsedBytes   int64  `json:"used_bytes"`
	QuotaBytes  int64  `json:"quota_bytes"`
	// Email and Phone are masked for the console; empty when unset.
	Email  string `json:"email"`
	Phone  string `json:"phone"`
	IsRoot bool   `json:"is_root"`
	// CanCreateGroups is the P1 delegation flag: the user may create groups and
	// manage the ones they created, nothing else.
	CanCreateGroups bool       `json:"can_create_groups"`
	ReservedBytes   int64      `json:"reserved_bytes"`
	CreatedAt       time.Time  `json:"created_at"`
	LastLoginAt     *time.Time `json:"last_login_at,omitempty"`
	LockedUntil     *time.Time `json:"locked_until,omitempty"`
	DeletedAt       *time.Time `json:"deleted_at"`
	PurgeAfter      *time.Time `json:"purge_after"`
}

type adminUsersResponse struct {
	Items  []adminUserJSON `json:"items"`
	Total  int             `json:"total"`
	Limit  int             `json:"limit"`
	Offset int             `json:"offset"`
}

type adminCreateUserRequest struct {
	Username   string `json:"username"`
	Email      string `json:"email"`
	Phone      string `json:"phone"`
	Password   string `json:"password"`
	Role       string `json:"role"`
	QuotaBytes int64  `json:"quota_bytes"`
}

type adminUpdateUserRequest struct {
	Status          *string `json:"status"`
	QuotaBytes      *int64  `json:"quota_bytes"`
	Role            *string `json:"role"`
	CanCreateGroups *bool   `json:"can_create_groups"`
}

type adminAuditJSON struct {
	At         time.Time `json:"at"`
	ActorID    int64     `json:"actor_id"`
	ActorName  string    `json:"actor_name"`
	Action     string    `json:"action"`
	TargetType string    `json:"target_type"`
	TargetID   int64     `json:"target_id"`
	Result     string    `json:"result"`
	IP         string    `json:"ip"`
	RequestID  string    `json:"request_id"`
	Detail     string    `json:"detail"`
}

type adminAuditResponse struct {
	Items  []adminAuditJSON `json:"items"`
	Total  int              `json:"total"`
	Limit  int              `json:"limit"`
	Offset int              `json:"offset"`
}

type adminSettingsJSON struct {
	SiteName            string `json:"site_name"`
	RegistrationEnabled bool   `json:"registration_enabled"`
	DeletionEnabled     bool   `json:"deletion_enabled"`
	DefaultQuotaBytes   int64  `json:"default_quota_bytes"`
	AuditRetentionDays  int    `json:"audit_retention_days"`
	TrashRetentionDays  int    `json:"trash_retention_days"`
}

type adminSettingsPatch struct {
	SiteName            *string `json:"site_name"`
	RegistrationEnabled *bool   `json:"registration_enabled"`
	DeletionEnabled     *bool   `json:"deletion_enabled"`
	DefaultQuotaBytes   *int64  `json:"default_quota_bytes"`
}

func toAdminUserJSON(u identity.User) adminUserJSON {
	return adminUserJSON{
		ID:              u.ID,
		Username:        u.Username,
		DisplayName:     u.DisplayName,
		Role:            u.RoleCode,
		Status:          u.Status,
		UsedBytes:       u.UsedBytes,
		QuotaBytes:      u.QuotaBytes,
		Email:           identity.MaskEmail(u.Email),
		Phone:           identity.MaskPhone(u.Phone),
		IsRoot:          u.IsRoot,
		CanCreateGroups: u.CanCreateGroups,
		ReservedBytes:   u.ReservedBytes,
		CreatedAt:       u.CreatedAt,
		LastLoginAt:     u.LastLoginAt,
		LockedUntil:     u.LockedUntil,
		DeletedAt:       u.DeletedAt,
		PurgeAfter:      u.PurgeAfter,
	}
}

func toAdminSettingsJSON(v adminsvc.Settings) adminSettingsJSON {
	return adminSettingsJSON{
		SiteName:            v.SiteName,
		RegistrationEnabled: v.RegistrationEnabled,
		DeletionEnabled:     v.DeletionEnabled,
		DefaultQuotaBytes:   v.DefaultQuotaBytes,
		AuditRetentionDays:  v.AuditRetentionDays,
		TrashRetentionDays:  v.TrashRetentionDays,
	}
}

func (s *Server) handleAdminListUsers(w http.ResponseWriter, r *http.Request) {
	p, err := requireUser(r)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	if err := s.authz.Require(p, access.ActionAdminUsers, access.SystemResource()); err != nil {
		s.writeError(w, r, err)
		return
	}
	limit, err := queryInt(r, "limit", 50, 1, 200)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	offset, err := queryInt(r, "offset", 0, 0, 1_000_000)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	page, err := s.admin.ListUsers(r.Context(), p, strings.TrimSpace(r.URL.Query().Get("q")), limit, offset)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	items := make([]adminUserJSON, 0, len(page.Items))
	for _, u := range page.Items {
		items = append(items, toAdminUserJSON(u))
	}
	s.writeJSON(w, http.StatusOK, adminUsersResponse{Items: items, Total: page.Total, Limit: limit, Offset: offset})
}

func (s *Server) handleAdminCreateUser(w http.ResponseWriter, r *http.Request) {
	p, err := requireUser(r)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	if err := s.authz.Require(p, access.ActionAdminUsers, access.SystemResource()); err != nil {
		s.writeError(w, r, err)
		return
	}
	var req adminCreateUserRequest
	if err := decodeJSON(w, r, &req); err != nil {
		s.writeError(w, r, err)
		return
	}
	u, password, err := s.admin.CreateUser(r.Context(), p, adminsvc.CreateRequest{
		Username:   req.Username,
		Email:      req.Email,
		Phone:      req.Phone,
		Password:   req.Password,
		Role:       req.Role,
		QuotaBytes: req.QuotaBytes,
	})
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	body := map[string]any{"user": toAdminUserJSON(u)}
	if req.Password == "" {
		body["initial_password"] = password
	}
	s.writeJSON(w, http.StatusCreated, body)
}

func (s *Server) handleAdminUpdateUser(w http.ResponseWriter, r *http.Request) {
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
	var req adminUpdateUserRequest
	if err := decodeJSON(w, r, &req); err != nil {
		s.writeError(w, r, err)
		return
	}
	u, err := s.admin.UpdateUser(r.Context(), p, id, adminsvc.UpdateRequest{
		Status:          req.Status,
		QuotaBytes:      req.QuotaBytes,
		Role:            req.Role,
		CanCreateGroups: req.CanCreateGroups,
	})
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"user": toAdminUserJSON(u)})
}

func (s *Server) handleAdminResetPassword(w http.ResponseWriter, r *http.Request) {
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
	password, err := s.admin.ResetPassword(r.Context(), p, id)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"password": password})
}

func (s *Server) handleAdminAudit(w http.ResponseWriter, r *http.Request) {
	p, err := requireUser(r)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	if err := s.authz.Require(p, access.ActionAdminAudit, access.SystemResource()); err != nil {
		s.writeError(w, r, err)
		return
	}
	limit, err := queryInt(r, "limit", 50, 1, 200)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	offset, err := queryInt(r, "offset", 0, 0, 1_000_000)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	f := sqlstoreAuditFilter(r)
	f.Limit, f.Offset = limit, offset
	page, err := s.admin.ListAudit(r.Context(), p, f)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	items := make([]adminAuditJSON, 0, len(page.Items))
	for _, ev := range page.Items {
		items = append(items, adminAuditJSON{
			At: ev.At, ActorID: ev.ActorID, ActorName: ev.ActorName, Action: ev.Action,
			TargetType: ev.TargetType, TargetID: ev.TargetID, Result: ev.Result,
			IP: ev.IP, RequestID: ev.RequestID, Detail: ev.Detail,
		})
	}
	s.writeJSON(w, http.StatusOK, adminAuditResponse{Items: items, Total: page.Total, Limit: limit, Offset: offset})
}

func (s *Server) handleAdminAuditActions(w http.ResponseWriter, r *http.Request) {
	p, err := requireUser(r)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	if err := s.authz.Require(p, access.ActionAdminAudit, access.SystemResource()); err != nil {
		s.writeError(w, r, err)
		return
	}
	actions, err := s.admin.AuditActions(r.Context(), p)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"actions": actions})
}

func (s *Server) handleAdminGetSettings(w http.ResponseWriter, r *http.Request) {
	p, err := requireUser(r)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	if err := s.authz.Require(p, access.ActionAdminSettings, access.SystemResource()); err != nil {
		s.writeError(w, r, err)
		return
	}
	v, err := s.admin.GetSettings(r.Context(), p)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	s.writeJSON(w, http.StatusOK, toAdminSettingsJSON(v))
}

func (s *Server) handleAdminPutSettings(w http.ResponseWriter, r *http.Request) {
	p, err := requireUser(r)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	if err := s.authz.Require(p, access.ActionAdminSettings, access.SystemResource()); err != nil {
		s.writeError(w, r, err)
		return
	}
	var req adminSettingsPatch
	if err := decodeJSON(w, r, &req); err != nil {
		s.writeError(w, r, err)
		return
	}
	v, err := s.admin.UpdateSettings(r.Context(), p, adminsvc.SettingsPatch{
		SiteName:            req.SiteName,
		RegistrationEnabled: req.RegistrationEnabled,
		DeletionEnabled:     req.DeletionEnabled,
		DefaultQuotaBytes:   req.DefaultQuotaBytes,
	})
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	s.writeJSON(w, http.StatusOK, toAdminSettingsJSON(v))
}
