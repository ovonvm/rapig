package httpx

import (
	"net/http"
	"strconv"
	"time"

	"rapig/internal/domain/access"
	domaingroups "rapig/internal/domain/groups"
	"rapig/internal/platform/apperr"

	groupsvc "rapig/internal/app/groups"
)

// ---- JSON shapes -----------------------------------------------------------

// groupJSON is the public shape of a group. member_perms is the permission set
// granted to plain members; managers always have every file action.
type groupJSON struct {
	ID          int64    `json:"id"`
	Name        string   `json:"name"`
	MemberPerms []string `json:"member_perms"`
	CreatedBy   *int64   `json:"created_by"`
	CreatedAt   string   `json:"created_at"`
	// RootID lets the client open /groups/{id} directly from any list.
	RootID int64 `json:"root_id"`
	// Role is only set on "my groups"; management lists leave it empty.
	Role        string `json:"role,omitempty"`
	MemberCount int    `json:"member_count"`
	NodeCount   int    `json:"node_count"`
}

func toGroupJSON(g domaingroups.Group) groupJSON {
	perms := make([]string, 0, len(g.MemberPerms))
	for _, a := range g.MemberPerms {
		perms = append(perms, string(a))
	}
	return groupJSON{
		ID:          g.ID,
		Name:        g.Name,
		MemberPerms: perms,
		CreatedBy:   g.CreatedBy,
		CreatedAt:   g.CreatedAt.UTC().Format(time.RFC3339),
		RootID:      g.RootID,
		Role:        string(g.Role),
		MemberCount: g.MemberCount,
		NodeCount:   g.NodeCount,
	}
}

type groupMemberJSON struct {
	UserID      int64  `json:"user_id"`
	Username    string `json:"username"`
	DisplayName string `json:"display_name"`
	Role        string `json:"role"`
}

func toGroupMemberJSON(m domaingroups.Member) groupMemberJSON {
	return groupMemberJSON{
		UserID:      m.UserID,
		Username:    m.Username,
		DisplayName: m.DisplayName,
		Role:        string(m.Role),
	}
}

type groupListResponse struct {
	Items []groupJSON `json:"items"`
}

type groupMemberListResponse struct {
	Items []groupMemberJSON `json:"items"`
}

type createGroupRequest struct {
	Name        string   `json:"name"`
	ManagerID   *int64   `json:"manager_id,omitempty"`
	MemberPerms []string `json:"member_perms,omitempty"`
}

type updateGroupRequest struct {
	Name        *string  `json:"name,omitempty"`
	MemberPerms []string `json:"member_perms,omitempty"`
}

type addGroupMemberRequest struct {
	UserID int64  `json:"user_id"`
	Role   string `json:"role"`
}

// ---- helpers ---------------------------------------------------------------

// groupManagementDenied reports whether the caller lacks every group-management
// capability: system admins (admin.users) and delegated users
// (can_create_groups) may call the management endpoints, everyone else gets 403.
func groupManagementDenied(p access.Principal) bool {
	return !p.Perms.Has(access.ActionAdminUsers) && !p.CanCreateGroups
}

// groupAdminAction picks the system action a group-admin endpoint checks: real
// admins must hold admin.users; users with the can_create_groups capability use
// the fallback action, which the policy grants on the system resource; everyone
// else is denied by mapping onto admin.users. The "you may only manage groups
// you created" part is enforced by the groups service, which maps escalation
// attempts to 404.
func groupAdminAction(p access.Principal, fallback access.Action) access.Action {
	if p.Perms.Has(access.ActionAdminUsers) {
		return access.ActionAdminUsers
	}
	if p.CanCreateGroups {
		return fallback
	}
	return access.ActionAdminUsers
}

// groupIDFromPath parses the {id} path segment as a group id.
func groupIDFromPath(r *http.Request) (int64, error) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		return 0, apperr.ErrBadRequest.WithReason(apperr.ReasonInvalidID)
	}
	return id, nil
}

// groupMemberUserIDFromPath parses the {user_id} path segment.
func groupMemberUserIDFromPath(r *http.Request) (int64, error) {
	id, err := strconv.ParseInt(r.PathValue("user_id"), 10, 64)
	if err != nil || id <= 0 {
		return 0, apperr.ErrBadRequest.WithReason(apperr.ReasonInvalidID)
	}
	return id, nil
}

// parseGroupPerms converts the wire format (["read","write"]) into actions.
// Unknown action names are a validation error, not silently dropped.
func parseGroupPerms(raw []string) ([]access.Action, error) {
	if raw == nil {
		return nil, nil
	}
	actions := make([]access.Action, 0, len(raw))
	for _, code := range raw {
		a, ok := access.ParseAction(code)
		if !ok {
			return nil, apperr.ErrGroupPermsInvalid.WithCause(apperr.ErrBadRequest)
		}
		actions = append(actions, a)
	}
	return actions, nil
}

// ---- user endpoints --------------------------------------------------------

// handleListGroups lists the groups the caller is a member of.
func (s *Server) handleListGroups(w http.ResponseWriter, r *http.Request) {
	p, err := requireUser(r)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	if err := s.authz.Require(p, access.ActionRead, access.SystemResource()); err != nil {
		s.writeError(w, r, err)
		return
	}
	items, err := s.groups.ListMyGroups(r.Context(), p)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	out := make([]groupJSON, 0, len(items))
	for _, g := range items {
		out = append(out, toGroupJSON(g))
	}
	s.writeJSON(w, http.StatusOK, groupListResponse{Items: out})
}

// handleCreateGroup creates a group the caller manages; it requires the
// can_create_groups capability (system admins implicitly have it).
func (s *Server) handleCreateGroup(w http.ResponseWriter, r *http.Request) {
	p, err := requireUser(r)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	if groupManagementDenied(p) {
		s.writeError(w, r, apperr.ErrForbidden)
		return
	}
	if err := s.authz.Require(p, groupAdminAction(p, access.ActionCreate), access.SystemResource()); err != nil {
		s.writeError(w, r, err)
		return
	}
	var req createGroupRequest
	if err := decodeJSON(w, r, &req); err != nil {
		s.writeError(w, r, err)
		return
	}
	perms, err := parseGroupPerms(req.MemberPerms)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	g, rootID, err := s.groups.CreateGroup(r.Context(), p, groupsvc.CreateRequest{
		Name: req.Name, MemberPerms: perms,
	})
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	s.audit(r, p, "group.create", "group", g.ID, mustJSON(map[string]any{
		"name": g.Name, "root_id": rootID,
	}))
	s.writeJSON(w, http.StatusCreated, map[string]any{"group": toGroupJSON(g), "root_id": rootID})
}

// ---- admin endpoints -------------------------------------------------------

// handleAdminListGroups lists groups visible to the caller's management scope:
// everything for a system admin, only self-created groups for a delegated user.
func (s *Server) handleAdminListGroups(w http.ResponseWriter, r *http.Request) {
	p, err := requireUser(r)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	if groupManagementDenied(p) {
		s.writeError(w, r, apperr.ErrForbidden)
		return
	}
	if err := s.authz.Require(p, groupAdminAction(p, access.ActionRead), access.SystemResource()); err != nil {
		s.writeError(w, r, err)
		return
	}
	items, err := s.groups.ListGroups(r.Context(), p)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	out := make([]groupJSON, 0, len(items))
	for _, g := range items {
		out = append(out, toGroupJSON(g))
	}
	s.writeJSON(w, http.StatusOK, groupListResponse{Items: out})
}

// handleAdminCreateGroup creates a group through the management API. An admin
// may name another manager for the group; a delegated user may not.
func (s *Server) handleAdminCreateGroup(w http.ResponseWriter, r *http.Request) {
	p, err := requireUser(r)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	if groupManagementDenied(p) {
		s.writeError(w, r, apperr.ErrForbidden)
		return
	}
	if err := s.authz.Require(p, groupAdminAction(p, access.ActionCreate), access.SystemResource()); err != nil {
		s.writeError(w, r, err)
		return
	}
	var req createGroupRequest
	if err := decodeJSON(w, r, &req); err != nil {
		s.writeError(w, r, err)
		return
	}
	perms, err := parseGroupPerms(req.MemberPerms)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	g, rootID, err := s.groups.CreateGroup(r.Context(), p, groupsvc.CreateRequest{
		Name: req.Name, ManagerID: req.ManagerID, MemberPerms: perms,
	})
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	s.audit(r, p, "group.create", "group", g.ID, mustJSON(map[string]any{
		"name": g.Name, "root_id": rootID, "manager_id": req.ManagerID,
	}))
	s.writeJSON(w, http.StatusCreated, map[string]any{"group": toGroupJSON(g), "root_id": rootID})
}

// handleAdminUpdateGroup renames a group and/or replaces its member_perms.
func (s *Server) handleAdminUpdateGroup(w http.ResponseWriter, r *http.Request) {
	p, err := requireUser(r)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	if groupManagementDenied(p) {
		s.writeError(w, r, apperr.ErrForbidden)
		return
	}
	if err := s.authz.Require(p, groupAdminAction(p, access.ActionCreate), access.SystemResource()); err != nil {
		s.writeError(w, r, err)
		return
	}
	id, err := groupIDFromPath(r)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	var req updateGroupRequest
	if err := decodeJSON(w, r, &req); err != nil {
		s.writeError(w, r, err)
		return
	}
	if req.Name == nil && req.MemberPerms == nil {
		s.writeError(w, r, apperr.ErrBadRequest)
		return
	}
	perms, err := parseGroupPerms(req.MemberPerms)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	g, err := s.groups.UpdateGroup(r.Context(), p, id, groupsvc.UpdateRequest{
		Name: req.Name, MemberPerms: perms,
	})
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	s.audit(r, p, "group.update", "group", g.ID, mustJSON(map[string]any{
		"name": g.Name, "member_perms": g.MemberPerms,
	}))
	s.writeJSON(w, http.StatusOK, map[string]any{"group": toGroupJSON(g)})
}

// handleAdminDeleteGroup deletes an empty group; a non-empty group is 409
// group_not_empty.
func (s *Server) handleAdminDeleteGroup(w http.ResponseWriter, r *http.Request) {
	p, err := requireUser(r)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	if groupManagementDenied(p) {
		s.writeError(w, r, apperr.ErrForbidden)
		return
	}
	if err := s.authz.Require(p, groupAdminAction(p, access.ActionCreate), access.SystemResource()); err != nil {
		s.writeError(w, r, err)
		return
	}
	id, err := groupIDFromPath(r)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	if err := s.groups.DeleteGroup(r.Context(), p, id); err != nil {
		s.writeError(w, r, err)
		return
	}
	s.audit(r, p, "group.delete", "group", id, "")
	w.WriteHeader(http.StatusNoContent)
}

// handleAdminListGroupMembers lists the members of a group.
func (s *Server) handleAdminListGroupMembers(w http.ResponseWriter, r *http.Request) {
	p, err := requireUser(r)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	if groupManagementDenied(p) {
		s.writeError(w, r, apperr.ErrForbidden)
		return
	}
	if err := s.authz.Require(p, groupAdminAction(p, access.ActionRead), access.SystemResource()); err != nil {
		s.writeError(w, r, err)
		return
	}
	id, err := groupIDFromPath(r)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	members, err := s.groups.ListMembers(r.Context(), p, id)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	out := make([]groupMemberJSON, 0, len(members))
	for _, m := range members {
		out = append(out, toGroupMemberJSON(m))
	}
	s.writeJSON(w, http.StatusOK, groupMemberListResponse{Items: out})
}

// handleAdminAddGroupMember adds a member (manager or member) to a group.
func (s *Server) handleAdminAddGroupMember(w http.ResponseWriter, r *http.Request) {
	p, err := requireUser(r)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	if groupManagementDenied(p) {
		s.writeError(w, r, apperr.ErrForbidden)
		return
	}
	if err := s.authz.Require(p, groupAdminAction(p, access.ActionCreate), access.SystemResource()); err != nil {
		s.writeError(w, r, err)
		return
	}
	id, err := groupIDFromPath(r)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	var req addGroupMemberRequest
	if err := decodeJSON(w, r, &req); err != nil {
		s.writeError(w, r, err)
		return
	}
	role, ok := domaingroups.ParseRole(req.Role)
	if !ok && req.Role != "" {
		s.writeError(w, r, apperr.ErrBadRequest.WithReason(apperr.ReasonInvalidRole))
		return
	}
	m, created, err := s.groups.AddMember(r.Context(), p, id, req.UserID, role)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	action := "group.member.add"
	status := http.StatusCreated
	if !created {
		action = "group.member.update"
		status = http.StatusOK
	}
	s.audit(r, p, action, "group", id, mustJSON(map[string]any{
		"user_id": m.UserID, "role": string(m.Role),
	}))
	s.writeJSON(w, status, map[string]any{"member": toGroupMemberJSON(m)})
}

// handleAdminRemoveGroupMember removes a member; removing the last manager is
// 409 last_group_manager.
func (s *Server) handleAdminRemoveGroupMember(w http.ResponseWriter, r *http.Request) {
	p, err := requireUser(r)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	if groupManagementDenied(p) {
		s.writeError(w, r, apperr.ErrForbidden)
		return
	}
	if err := s.authz.Require(p, groupAdminAction(p, access.ActionCreate), access.SystemResource()); err != nil {
		s.writeError(w, r, err)
		return
	}
	id, err := groupIDFromPath(r)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	userID, err := groupMemberUserIDFromPath(r)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	if err := s.groups.RemoveMember(r.Context(), p, id, userID); err != nil {
		s.writeError(w, r, err)
		return
	}
	s.audit(r, p, "group.member.remove", "group", id, mustJSON(map[string]any{"user_id": userID}))
	w.WriteHeader(http.StatusNoContent)
}
