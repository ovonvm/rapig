package access

// Reason values returned by the policy layer. They are stable machine hints;
// the transport layer maps them onto HTTP responses and must never expose
// whether a foreign resource exists.
const (
	ReasonAllowed          = "allowed"
	ReasonOwner            = "owner"
	ReasonPermission       = "permission"
	ReasonSystem           = "system"
	ReasonAnonymous        = "anonymous"
	ReasonNotOwner         = "not_owner"
	ReasonPermissionDenied = "permission_denied"
	// ReasonGroupRole says the decision came from a group role (P1).
	ReasonGroupRole = "group_role"
)

// Check evaluates the policy matrix for one action on one resource.
//
// Matrix:
//
//	                    own node      other's node        group node        system
//	guest/anonymous     deny          deny                deny              deny
//	user (with perm)    allow         deny (not_owner)    role perms        deny
//	admin               allow         deny (not_owner)    role perms        deny
//	system              allow (explicit scope enforced by repositories)
//
// A group node is never reachable through ownership: the caller must be a
// member of that group and the role must grant the action, so a system admin
// without membership gets a deny just like everybody else.
func Check(p Principal, a Action, r Resource) Decision {
	if !a.Valid() {
		return Deny(ReasonPermissionDenied)
	}
	if p.System {
		return Allow(ReasonSystem)
	}

	switch r.Kind {
	case ResourceSystem:
		if p.Anonymous {
			return Deny(ReasonAnonymous)
		}
		if p.Perms.Has(a) {
			return Allow(ReasonPermission)
		}
		// 组管理能力（users.can_create_groups）在系统资源上放行读/建：
		// 仅用于组管理端点（/groups、/admin/groups*），由 groups 服务再校验
		// “只能管理自己创建的组”。
		if p.CanCreateGroups && (a == ActionRead || a == ActionCreate) {
			return Allow(ReasonPermission)
		}
		return Deny(ReasonPermissionDenied)

	case ResourceNode:
		if p.Anonymous {
			// Share links are a separate capability; there is no anonymous
			// access to nodes.
			return Deny(ReasonAnonymous)
		}
		if r.GroupID != nil {
			info, ok := p.GroupRoles[*r.GroupID]
			if !ok {
				// Non-members are simply not found: there is no admin bypass.
				return Deny(ReasonGroupRole)
			}
			if !info.Perms.Has(a) {
				return Deny(ReasonPermissionDenied)
			}
			return Allow(ReasonGroupRole)
		}
		if p.UserID > 0 && r.OwnerID == p.UserID {
			return Allow(ReasonOwner)
		}
		// Another user's node is simply not found: no admin bypass exists.
		return Deny(ReasonNotOwner)

	default:
		return Deny(ReasonPermissionDenied)
	}
}
