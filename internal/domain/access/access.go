// Package access is rapig's authorization core: principals, actions,
// resources and the policy matrix.
//
// Three hard rules make authorization enforceable:
//
//  1. Every repository method that touches user data takes a Scope and calls
//     Scope.Validate() as its first statement; an empty scope is a programmer
//     error, never "all data".
//  2. Every transport handler that touches a resource asks the Authorizer
//     before doing so. An AST based test (handler_guard_test.go) fails the
//     build when a handler forgets this.
//  3. Denials on another user's resource are mapped to 404, so the API never
//     leaks whether a foreign object exists.
package access

import (
	"fmt"
	"sort"
	"strings"

	"rapig/internal/platform/apperr"
)

// Action is an operation checked by the policy layer.
type Action string

const (
	ActionRead   Action = "read"
	ActionWrite  Action = "write"
	ActionCreate Action = "create"
	ActionDelete Action = "delete"
	ActionShare  Action = "share"
	ActionUpload Action = "upload"

	ActionAdminUsers    Action = "admin.users"
	ActionAdminSettings Action = "admin.settings"
	ActionAdminStorages Action = "admin.storages"
	ActionAdminAudit    Action = "admin.audit"
)

// AllActions lists every action understood by the policy layer.
var AllActions = []Action{
	ActionRead, ActionWrite, ActionCreate, ActionDelete, ActionShare, ActionUpload,
	ActionAdminUsers, ActionAdminSettings, ActionAdminStorages, ActionAdminAudit,
}

// Valid reports whether a is a known action.
func (a Action) Valid() bool {
	for _, known := range AllActions {
		if a == known {
			return true
		}
	}
	return false
}

// ParseAction converts a permission code into an Action.
func ParseAction(code string) (Action, bool) {
	a := Action(strings.TrimSpace(code))
	if !a.Valid() {
		return "", false
	}
	return a, true
}

// PermSet is the set of actions granted to a principal.
type PermSet map[Action]bool

// NewPermSet builds a permission set from actions.
func NewPermSet(actions ...Action) PermSet {
	s := make(PermSet, len(actions))
	for _, a := range actions {
		s[a] = true
	}
	return s
}

// Has reports whether a is granted.
func (s PermSet) Has(a Action) bool { return s != nil && s[a] }

// List returns the granted actions sorted, for logs and /me style responses.
func (s PermSet) List() []Action {
	out := make([]Action, 0, len(s))
	for a, ok := range s {
		if ok {
			out = append(out, a)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// Kind identifies the resource family checked by the policy.
type Kind string

const (
	ResourceNode   Kind = "node"
	ResourceSystem Kind = "system"
)

// Resource is the object an action is checked against.
//
// GroupID 非空表示组空间里的节点：这时 OwnerID 只用于审计（上传者/创建者），
// 真正的判定看 Principal.GroupRoles。GroupID 为空则是个人节点，沿用 owner
// 匹配。
type Resource struct {
	Kind    Kind
	ID      int64
	OwnerID int64
	GroupID *int64
}

// SystemResource is the singleton resource for site administration.
func SystemResource() Resource { return Resource{Kind: ResourceSystem} }

// GroupRole 是组内角色。它定义在 access 里而不是 domain/groups，因为策略层
// 要直接读它，而 domain/groups 反过来依赖 access。
type GroupRole string

const (
	// GroupRoleManager 是组管理者：恒定拥有全部文件动作。
	GroupRoleManager GroupRole = "manager"
	// GroupRoleMember 是普通成员：动作受组的 member_perms 限制。
	GroupRoleMember GroupRole = "member"
)

// Valid 报告角色是否是已知角色。
func (r GroupRole) Valid() bool { return r == GroupRoleManager || r == GroupRoleMember }

// GroupRoleInfo 是调用者在某个组里的角色与由此推出的权限。
type GroupRoleInfo struct {
	Role  GroupRole
	Perms PermSet
}

// Principal is the authenticated (or anonymous) caller.
type Principal struct {
	UserID    int64
	Username  string
	Role      string
	Perms     PermSet
	Anonymous bool
	System    bool

	// GroupRoles 是调用者加入的组及组内权限，登录时一次性装载。
	// 组资源只凭它放行：非成员（含系统 admin）一律 Deny。
	GroupRoles map[int64]GroupRoleInfo
	// CanCreateGroups 对应 users.can_create_groups，决定能否自助建组。
	CanCreateGroups bool
}

// AnonymousPrincipal is the caller without a session.
func AnonymousPrincipal() Principal { return Principal{Anonymous: true} }

// SystemPrincipal is used by trusted internal jobs; it is never built from
// network input.
func SystemPrincipal() Principal { return Principal{System: true} }

// Decision is the outcome of a policy check.
type Decision struct {
	Allow  bool
	Reason string
}

// Allow builds an allowing decision.
func Allow(reason string) Decision { return Decision{Allow: true, Reason: reason} }

// Deny builds a denying decision.
func Deny(reason string) Decision { return Decision{Allow: false, Reason: reason} }

// Scope bounds every user data query. It is deliberately not optional: an
// empty scope must fail loudly instead of silently scanning all rows.
type Scope struct {
	UserID    int64
	GroupIDs  []int64
	Admin     bool
	Anonymous bool
	System    bool
}

// Validate enforces that a scope has exactly one identity. Repository methods
// must call it first.
func (s Scope) Validate() error {
	switch {
	case s.System:
		if s.UserID != 0 || s.Anonymous || len(s.GroupIDs) > 0 {
			return apperr.ErrInvalidScope.WithCause(fmt.Errorf("system scope must be exclusive"))
		}
		return nil
	case s.Anonymous:
		if s.UserID != 0 || s.Admin || len(s.GroupIDs) > 0 {
			return apperr.ErrInvalidScope.WithCause(fmt.Errorf("anonymous scope must be exclusive"))
		}
		return nil
	case s.UserID > 0:
		return nil
	default:
		return apperr.ErrInvalidScope.WithCause(fmt.Errorf("scope needs a user, anonymous or system identity"))
	}
}

// UserScope builds the scope of an authenticated user.
func UserScope(userID int64, admin bool) Scope {
	return Scope{UserID: userID, Admin: admin}
}

// UserGroupScope builds the scope of a user that also sees the given groups.
// Background jobs (uploads, transfers, public share reads) work on behalf of an
// owner and must include the groups that owner belongs to. The slice is copied
// and sorted so the derived SQL argument order is stable.
func UserGroupScope(userID int64, admin bool, groupIDs []int64) Scope {
	return Scope{UserID: userID, Admin: admin, GroupIDs: normalizeGroupIDs(groupIDs)}
}

// ScopeFor derives the storage scope of a principal.
func ScopeFor(p Principal) Scope {
	return Scope{
		UserID:    p.UserID,
		GroupIDs:  p.visibleGroupIDs(),
		Admin:     p.Role == "admin",
		Anonymous: p.Anonymous,
		System:    p.System,
	}
}

// visibleGroupIDs lists the groups a principal can read nodes from, sorted for
// deterministic SQL argument order.
func (p Principal) visibleGroupIDs() []int64 {
	if p.UserID <= 0 || len(p.GroupRoles) == 0 {
		return nil
	}
	ids := make([]int64, 0, len(p.GroupRoles))
	for id := range p.GroupRoles {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids
}

// normalizeGroupIDs deduplicates and sorts group ids, dropping non-positive
// values (they can never match a row).
func normalizeGroupIDs(ids []int64) []int64 {
	if len(ids) == 0 {
		return nil
	}
	seen := make(map[int64]bool, len(ids))
	out := make([]int64, 0, len(ids))
	for _, id := range ids {
		if id <= 0 || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}
