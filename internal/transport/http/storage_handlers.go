package httpx

import (
	"net/http"
	"strconv"
	"time"

	adminsvc "rapig/internal/app/admin"
	"rapig/internal/domain/access"
	sqlstore "rapig/internal/infra/sql"
	"rapig/internal/platform/apperr"
)

// storagePolicyJSON is the admin view of one storage policy root.
type storagePolicyJSON struct {
	ID        int64  `json:"id"`
	Name      string `json:"name"`
	Type      string `json:"type"`
	RootPath  string `json:"root_path"`
	Enabled   bool   `json:"enabled"`
	IsDefault bool   `json:"is_default"`
	UsedBytes int64  `json:"used_bytes"`
	// NodeCount counts live nodes in the policy, FileCount only file nodes.
	NodeCount int64     `json:"node_count"`
	FileCount int64     `json:"file_count"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// userStoragePolicyJSON is the read-only list shown to any signed-in user: it
// says where new root-level folders can be created, nothing more.
type userStoragePolicyJSON struct {
	ID        int64  `json:"id"`
	Name      string `json:"name"`
	IsDefault bool   `json:"is_default"`
	Enabled   bool   `json:"enabled"`
}

func localStoragePolicyJSON(p sqlstore.StoragePolicy) storagePolicyJSON {
	return storagePolicyJSON{
		ID:        p.ID,
		Name:      p.Name,
		Type:      p.Type,
		RootPath:  p.RootPath,
		Enabled:   p.Enabled,
		IsDefault: p.IsDefault,
		UsedBytes: p.UsedBytes,
		NodeCount: p.NodeCount,
		FileCount: p.FileCount,
		CreatedAt: p.CreatedAt,
		UpdatedAt: p.UpdatedAt,
	}
}

// handleAdminListStorage lists every policy, disabled ones included.
func (s *Server) handleAdminListStorage(w http.ResponseWriter, r *http.Request) {
	p, err := requireUser(r)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	if err := s.authz.Require(p, access.ActionAdminStorages, access.SystemResource()); err != nil {
		s.writeError(w, r, err)
		return
	}
	policies, err := s.admin.ListStoragePolicies(r.Context())
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	items := make([]storagePolicyJSON, 0, len(policies))
	for _, policy := range policies {
		items = append(items, localStoragePolicyJSON(policy))
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

// handleAdminCreateStorage adds a root. The path is validated and probed by the
// admin service before the row exists.
func (s *Server) handleAdminCreateStorage(w http.ResponseWriter, r *http.Request) {
	p, err := requireUser(r)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	if err := s.authz.Require(p, access.ActionAdminStorages, access.SystemResource()); err != nil {
		s.writeError(w, r, err)
		return
	}
	var req struct {
		Name      string `json:"name"`
		RootPath  string `json:"root_path"`
		Enabled   *bool  `json:"enabled"`
		IsDefault *bool  `json:"is_default"`
	}
	if err := decodeJSON(w, r, &req); err != nil {
		s.writeError(w, r, err)
		return
	}
	policy, err := s.admin.CreateStoragePolicy(r.Context(), adminsvc.StoragePolicyRequest{
		Name:      req.Name,
		RootPath:  req.RootPath,
		Enabled:   req.Enabled,
		IsDefault: req.IsDefault,
	})
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	s.registry.Invalidate(policy.ID)
	s.audit(r, p, "storage.create", "storage_policy", policy.ID, mustJSON(map[string]any{
		"name": policy.Name, "root_path": policy.RootPath, "is_default": policy.IsDefault,
	}))
	s.writeJSON(w, http.StatusCreated, map[string]any{"policy": localStoragePolicyJSON(policy)})
}

// handleAdminUpdateStorage patches one policy. The registry cache is dropped
// afterwards so a new root_path or enabled flag takes effect immediately.
func (s *Server) handleAdminUpdateStorage(w http.ResponseWriter, r *http.Request) {
	p, err := requireUser(r)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	if err := s.authz.Require(p, access.ActionAdminStorages, access.SystemResource()); err != nil {
		s.writeError(w, r, err)
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		s.writeError(w, r, apperr.ErrBadRequest.WithReason(apperr.ReasonInvalidID))
		return
	}
	var req struct {
		Name      *string `json:"name"`
		RootPath  *string `json:"root_path"`
		Enabled   *bool   `json:"enabled"`
		IsDefault *bool   `json:"is_default"`
	}
	if err := decodeJSON(w, r, &req); err != nil {
		s.writeError(w, r, err)
		return
	}
	policy, err := s.admin.UpdateStoragePolicy(r.Context(), id, adminsvc.StoragePolicyPatch{
		Name:      req.Name,
		RootPath:  req.RootPath,
		Enabled:   req.Enabled,
		IsDefault: req.IsDefault,
	})
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	s.registry.Invalidate(id)
	s.audit(r, p, "storage.update", "storage_policy", id, mustJSON(map[string]any{
		"name": policy.Name, "root_path": policy.RootPath,
		"enabled": policy.Enabled, "is_default": policy.IsDefault,
	}))
	s.writeJSON(w, http.StatusOK, map[string]any{"policy": localStoragePolicyJSON(policy)})
}

// handleAdminDeleteStorage removes an empty, non-default policy. The data
// directory itself is left in place: deleting files is the operator's job.
func (s *Server) handleAdminDeleteStorage(w http.ResponseWriter, r *http.Request) {
	p, err := requireUser(r)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	if err := s.authz.Require(p, access.ActionAdminStorages, access.SystemResource()); err != nil {
		s.writeError(w, r, err)
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		s.writeError(w, r, apperr.ErrBadRequest.WithReason(apperr.ReasonInvalidID))
		return
	}
	if err := s.admin.DeleteStoragePolicy(r.Context(), id); err != nil {
		s.writeError(w, r, err)
		return
	}
	s.registry.Invalidate(id)
	s.audit(r, p, "storage.delete", "storage_policy", id, "")
	w.WriteHeader(http.StatusNoContent)
}

// handleStoragePolicies lists the enabled policies for any signed-in user, so
// the file browser can offer "create folder in…" without admin rights.
func (s *Server) handleStoragePolicies(w http.ResponseWriter, r *http.Request) {
	if _, err := requireUser(r); err != nil {
		s.writeError(w, r, err)
		return
	}
	policies, err := s.admin.EnabledStoragePolicies(r.Context())
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	items := make([]userStoragePolicyJSON, 0, len(policies))
	for _, policy := range policies {
		items = append(items, userStoragePolicyJSON{
			ID:        policy.ID,
			Name:      policy.Name,
			IsDefault: policy.IsDefault,
			Enabled:   policy.Enabled,
		})
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"items": items})
}
