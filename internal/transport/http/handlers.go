package httpx

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"rapig/internal/domain/access"
	domainfiles "rapig/internal/domain/files"
	domainidentity "rapig/internal/domain/identity"
	"rapig/internal/infra/sql"
	"rapig/internal/platform/apperr"
	"rapig/internal/platform/version"

	idsvc "rapig/internal/app/identity"
)

// ---- JSON shapes -----------------------------------------------------------

type userJSON struct {
	ID          int64  `json:"id"`
	Username    string `json:"username"`
	DisplayName string `json:"display_name"`
	Role        string `json:"role"`
	UsedBytes   int64  `json:"used_bytes"`
	QuotaBytes  int64  `json:"quota_bytes"`
	// Email and Phone are the caller's own identifiers, never masked here.
	Email  string `json:"email"`
	Phone  string `json:"phone"`
	IsRoot bool   `json:"is_root"`
	// CanCreateGroups lets the SPA decide whether to offer "new group" and the
	// group management screen; the server enforces it again on every write.
	CanCreateGroups bool `json:"can_create_groups"`
}

func toUserJSON(u domainidentity.User) userJSON {
	return userJSON{
		ID:              u.ID,
		Username:        u.Username,
		DisplayName:     u.DisplayName,
		Role:            u.RoleCode,
		UsedBytes:       u.UsedBytes,
		QuotaBytes:      u.QuotaBytes,
		Email:           u.Email,
		Phone:           u.Phone,
		IsRoot:          u.IsRoot,
		CanCreateGroups: u.CanCreateGroups,
	}
}

type nodeJSON struct {
	ID        int64  `json:"id"`
	ParentID  int64  `json:"parent_id"`
	Kind      string `json:"kind"`
	Name      string `json:"name"`
	Size      int64  `json:"size"`
	Revision  string `json:"revision"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
	// GroupID marks a node that lives in a group space; personal nodes omit it
	// so existing clients keep seeing exactly the same shape.
	GroupID *int64 `json:"group_id,omitempty"`
}

func toNodeJSON(n domainfiles.Node) nodeJSON {
	parent := int64(0)
	if n.ParentID != nil {
		parent = *n.ParentID
	}
	return nodeJSON{
		ID:        n.ID,
		ParentID:  parent,
		Kind:      string(n.Kind),
		Name:      n.Name,
		Size:      n.Size,
		Revision:  n.Revision(),
		CreatedAt: n.CreatedAt.UTC().Format(time.RFC3339),
		UpdatedAt: n.UpdatedAt.UTC().Format(time.RFC3339),
		GroupID:   n.GroupID,
	}
}

// nodeInParent renders a node as seen from parent. The API references the
// caller's root as parent 0, so direct children of the root report 0 instead of
// the internal root row id (which clients never see).
func nodeInParent(n, parent domainfiles.Node) nodeJSON {
	out := toNodeJSON(n)
	if parent.IsRoot() {
		out.ParentID = 0
	} else {
		out.ParentID = parent.ID
	}
	return out
}

type listNodesResponse struct {
	Items  []nodeJSON `json:"items"`
	Total  int        `json:"total"`
	Limit  int        `json:"limit"`
	Offset int        `json:"offset"`
}

// ---- handlers --------------------------------------------------------------

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if err := s.db.Ping(ctx); err != nil {
		s.log.Error("health: database ping failed", "err", err, "request_id", requestID(r))
		s.writeJSON(w, http.StatusServiceUnavailable, map[string]any{
			"status": "degraded", "db": "error", "version": version.Version,
		})
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{
		"status": "ok", "db": "ok", "version": version.Version,
	})
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := decodeJSON(w, r, &req); err != nil {
		s.writeError(w, r, err)
		return
	}
	res, err := s.identity.Login(r.Context(), idsvc.LoginRequest{
		Username:  req.Username,
		Password:  req.Password,
		IP:        clientIP(r),
		UserAgent: r.UserAgent(),
		RequestID: requestID(r),
	})
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	s.setLoginCookies(w, r, res.SessionToken, res.CSRFToken, res.ExpiresAt)
	s.writeJSON(w, http.StatusOK, map[string]any{"user": toUserJSON(res.User)})
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	err := s.identity.Logout(r.Context(), sessionToken(r), idsvc.LoginRequest{
		IP:        clientIP(r),
		UserAgent: r.UserAgent(),
		RequestID: requestID(r),
	})
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	clearAuthCookies(w)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	p, err := requireUser(r)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	user, err := s.identity.User(r.Context(), p.UserID)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"user": toUserJSON(user)})
}

func (s *Server) handleListNodes(w http.ResponseWriter, r *http.Request) {
	p, err := requireUser(r)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	parentRef, err := queryInt64(r, "parent", 0)
	if err != nil {
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

	parent, err := s.files.Parent(r.Context(), p, parentRef)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	if err := s.authz.Require(p, access.ActionRead, access.Resource{
		Kind: access.ResourceNode, ID: parent.ID, OwnerID: parent.OwnerID, GroupID: parent.GroupID,
	}); err != nil {
		s.writeError(w, r, err)
		return
	}
	items, total, err := s.files.Children(r.Context(), p, parent, limit, offset)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	out := make([]nodeJSON, 0, len(items))
	for _, n := range items {
		out = append(out, nodeInParent(n, parent))
	}
	s.writeJSON(w, http.StatusOK, listNodesResponse{
		Items: out, Total: total, Limit: limit, Offset: offset,
	})
}

// handleSearchNodes searches the caller's own nodes by filename. The query is
// capped at 100 runes; results expose internal parent ids except direct
// children of the root, which map to the API's parent 0.
func (s *Server) handleSearchNodes(w http.ResponseWriter, r *http.Request) {
	p, err := requireUser(r)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	if err := s.authz.Require(p, access.ActionRead, access.SystemResource()); err != nil {
		s.writeError(w, r, err)
		return
	}
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	if query == "" {
		s.writeError(w, r, apperr.ErrBadRequest.WithReason("query_required"))
		return
	}
	if utf8.RuneCountInString(query) > 100 {
		s.writeError(w, r, apperr.ErrBadRequest.WithReason(apperr.ReasonOutOfRange))
		return
	}
	kind := r.URL.Query().Get("type")
	if kind != "" && kind != string(domainfiles.KindFile) && kind != string(domainfiles.KindDir) {
		s.writeError(w, r, apperr.ErrBadRequest.WithReason("invalid_type"))
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
	recent := r.URL.Query().Get("recent")
	recentOrder := recent == "1" || recent == "true"
	// group_id narrows the search to one group space. It is optional: without
	// it the search stays the caller's personal tree, exactly as before P1.
	groupID, err := queryInt64(r, "group_id", 0)
	if err != nil {
		s.writeError(w, r, err)
		return
	}

	var (
		root  domainfiles.Node
		items []domainfiles.Node
		total int
	)
	if groupID > 0 {
		gid := groupID
		if err := s.authz.Require(p, access.ActionRead, access.Resource{
			Kind: access.ResourceNode, GroupID: &gid,
		}); err != nil {
			s.writeError(w, r, err)
			return
		}
		root, err = s.files.GroupRoot(r.Context(), p, gid)
		if err != nil {
			s.writeError(w, r, err)
			return
		}
		items, total, err = s.files.SearchGroup(r.Context(), p, gid, query, kind, recentOrder, limit, offset)
		if err != nil {
			s.writeError(w, r, err)
			return
		}
	} else {
		root, err = s.files.Parent(r.Context(), p, 0)
		if err != nil {
			s.writeError(w, r, err)
			return
		}
		items, total, err = s.files.Search(r.Context(), p, query, kind, recentOrder, limit, offset)
		if err != nil {
			s.writeError(w, r, err)
			return
		}
	}
	out := make([]nodeJSON, 0, len(items))
	for _, n := range items {
		item := toNodeJSON(n)
		if n.ParentID == nil || *n.ParentID == root.ID {
			item.ParentID = 0
		}
		out = append(out, item)
	}
	s.writeJSON(w, http.StatusOK, listNodesResponse{
		Items: out, Total: total, Limit: limit, Offset: offset,
	})
}

func (s *Server) handleCreateNode(w http.ResponseWriter, r *http.Request) {
	p, err := requireUser(r)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	var req struct {
		Parent int64  `json:"parent"`
		Name   string `json:"name"`
		Kind   string `json:"kind"`
		// StoragePolicyID is only meaningful for a directory created directly
		// under the account root (M5.3): it moves that subtree onto another
		// policy root. Every other node inherits its parent policy.
		StoragePolicyID *int64 `json:"storage_policy_id"`
	}
	if err := decodeJSON(w, r, &req); err != nil {
		s.writeError(w, r, err)
		return
	}
	kind := req.Kind
	if kind == "" {
		kind = string(domainfiles.KindDir)
	}
	if kind != string(domainfiles.KindDir) {
		// Uploads land in M1; reject early instead of half implementing them.
		s.writeError(w, r, apperr.New(apperr.CodeBadRequest, http.StatusBadRequest,
			"M0 仅支持新建文件夹").WithReason(apperr.ReasonOnlyDirectory))
		return
	}

	parent, err := s.files.Parent(r.Context(), p, req.Parent)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	if err := s.authz.Require(p, access.ActionCreate, access.Resource{
		Kind: access.ResourceNode, ID: parent.ID, OwnerID: parent.OwnerID, GroupID: parent.GroupID,
	}); err != nil {
		s.writeError(w, r, err)
		return
	}
	node, err := s.files.CreateDir(r.Context(), p, parent, req.Name, req.StoragePolicyID)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	s.audit(r, p, "node.create", "node", node.ID, mustJSON(map[string]any{
		"parent_id": parent.ID,
		"kind":      string(node.Kind),
		"name":      node.Name,
	}))
	s.writeJSON(w, http.StatusCreated, nodeInParent(node, parent))
}

// ---- helpers ---------------------------------------------------------------

func requireUser(r *http.Request) (access.Principal, error) {
	p, ok := principalFrom(r)
	if !ok || p.Anonymous || p.UserID <= 0 {
		return access.Principal{}, apperr.ErrUnauthenticated
	}
	return p, nil
}

func queryInt(r *http.Request, key string, def, min, max int) (int, error) {
	raw := strings.TrimSpace(r.URL.Query().Get(key))
	if raw == "" {
		return def, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return 0, apperr.ErrBadRequest.WithCause(err)
	}
	if n < min || n > max {
		return 0, apperr.ErrBadRequest.WithReason(apperr.ReasonOutOfRange)
	}
	return n, nil
}

func queryInt64(r *http.Request, key string, def int64) (int64, error) {
	raw := strings.TrimSpace(r.URL.Query().Get(key))
	if raw == "" {
		return def, nil
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || n < 0 {
		return 0, apperr.ErrBadRequest.WithReason(apperr.ReasonInvalidID)
	}
	return n, nil
}

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func (s *Server) audit(r *http.Request, p access.Principal, action, targetType string, targetID int64, detail string) {
	err := s.db.InsertAudit(r.Context(), sqlstore.AuditEvent{
		At:         time.Now().UTC(),
		ActorID:    p.UserID,
		ActorName:  p.Username,
		Action:     action,
		TargetType: targetType,
		TargetID:   targetID,
		Result:     "success",
		IP:         clientIP(r),
		UserAgent:  truncateText(r.UserAgent(), 512),
		RequestID:  requestID(r),
		Detail:     detail,
	})
	if err != nil {
		s.log.Warn("audit write failed", "err", err, "action", action, "request_id", requestID(r))
	}
}

func truncateText(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max]
}

func mustJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return string(b)
}
