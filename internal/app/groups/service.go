// Package groups 实现组共享空间的应用服务：建组、改组、删组、成员管理与
// 组管理权限判定。
//
// 权限模型（docs/20-p1-design.md §3）：
//
//   - 系统 admin（拥有 admin.users）可以创建并管理全部组；
//   - 被授予 can_create_groups 的普通用户可以创建组，但只能管理自己
//     created_by 的组，越界一律按 404 处理（不泄露组是否存在）；
//   - 组成员身份（manager/member）只决定组内文件动作，不授予管理接口权限。
package groups

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"rapig/internal/domain/access"
	domaingroups "rapig/internal/domain/groups"
	"rapig/internal/infra/sql"
	"rapig/internal/platform/apperr"
)

// Service 是组共享空间的应用服务。
type Service struct {
	db  *sqlstore.DB
	log *slog.Logger
	now func() time.Time
}

// NewService 构造组服务。now 可注入，便于测试；生产用 time.Now。
func NewService(db *sqlstore.DB, log *slog.Logger) *Service {
	return &Service{db: db, log: log, now: func() time.Time { return time.Now().UTC() }}
}

// CreateRequest 是 POST /groups 的输入。ManagerID 只有系统 admin 可以指定，
// 用于“管理端替某个用户建组”；普通建组者固定成为 manager。
type CreateRequest struct {
	Name        string
	ManagerID   *int64
	MemberPerms []access.Action
}

// UpdateRequest 是 PATCH /admin/groups/{id} 的输入；nil 字段保持原值。
type UpdateRequest struct {
	Name        *string
	MemberPerms []access.Action
}

// CreateGroup 创建一个组及其根节点，调用者（或 admin 指定的 ManagerID）成为
// 首位 manager。返回组和组根节点 id。
func (s *Service) CreateGroup(ctx context.Context, p access.Principal, req CreateRequest) (domaingroups.Group, int64, error) {
	if !s.canCreate(p) {
		return domaingroups.Group{}, 0, apperr.ErrForbidden
	}
	adminPerm := p.Perms.Has(access.ActionAdminUsers)
	managerID := p.UserID
	if req.ManagerID != nil && *req.ManagerID != p.UserID {
		if !adminPerm {
			return domaingroups.Group{}, 0, apperr.ErrForbidden
		}
		managerID = *req.ManagerID
	}
	if managerID != p.UserID {
		// 被指派的 manager 必须是现存账号，否则组会没有管理者。
		if _, err := s.db.UserByID(ctx, managerID); err != nil {
			if apperr.CodeOf(err) == apperr.CodeNotFound {
				return domaingroups.Group{}, 0, apperr.ErrBadRequest.WithReason(apperr.ReasonInvalidID)
			}
			return domaingroups.Group{}, 0, err
		}
	}
	name, err := domaingroups.ValidateName(req.Name)
	if err != nil {
		return domaingroups.Group{}, 0, err
	}
	perms := req.MemberPerms
	if perms == nil {
		perms = domaingroups.DefaultMemberPerms()
	}
	normalized, err := domaingroups.NormalizePerms(perms)
	if err != nil {
		return domaingroups.Group{}, 0, apperr.ErrGroupPermsInvalid.WithCause(err)
	}
	folded := domaingroups.FoldName(name)
	if _, err := s.db.GroupByNameFolded(ctx, folded); err == nil {
		return domaingroups.Group{}, 0, apperr.ErrNameConflict
	} else if apperr.CodeOf(err) != apperr.CodeNotFound {
		return domaingroups.Group{}, 0, err
	}
	creator := p.UserID
	g := domaingroups.Group{
		Name:        name,
		NameFolded:  folded,
		CreatedBy:   &creator,
		MemberPerms: normalized,
		CreatedAt:   s.now(),
	}
	created, rootID, err := s.db.CreateGroup(ctx, g, managerID)
	if err != nil {
		return domaingroups.Group{}, 0, err
	}
	return created, rootID, nil
}

// ListGroups 返回调用者可见的管理视图：admin 看全部，被指派用户只看自己创建
// 的组。
func (s *Service) ListGroups(ctx context.Context, p access.Principal) ([]domaingroups.Group, error) {
	if p.Perms.Has(access.ActionAdminUsers) {
		return s.db.ListGroups(ctx)
	}
	if p.CanCreateGroups {
		return s.db.ListGroupsCreatedBy(ctx, p.UserID)
	}
	return nil, apperr.ErrForbidden
}

// ListMyGroups 返回用户加入的全部组（成员视角，不区分是否自己创建）。
func (s *Service) ListMyGroups(ctx context.Context, p access.Principal) ([]domaingroups.Group, error) {
	if p.UserID <= 0 {
		return nil, apperr.ErrUnauthenticated
	}
	return s.db.ListGroupsForUser(ctx, p.UserID)
}

// UpdateGroup 改名和/或改 member_perms。仅组创建者或系统 admin 可以调用。
func (s *Service) UpdateGroup(ctx context.Context, p access.Principal, groupID int64, req UpdateRequest) (domaingroups.Group, error) {
	g, err := s.manageableGroup(ctx, p, groupID)
	if err != nil {
		return domaingroups.Group{}, err
	}
	var name *string
	if req.Name != nil {
		trimmed, err := domaingroups.ValidateName(*req.Name)
		if err != nil {
			return domaingroups.Group{}, err
		}
		if folded := domaingroups.FoldName(trimmed); folded != g.NameFolded {
			if other, err := s.db.GroupByNameFolded(ctx, folded); err == nil && other.ID != g.ID {
				return domaingroups.Group{}, apperr.ErrNameConflict
			} else if err != nil && apperr.CodeOf(err) != apperr.CodeNotFound {
				return domaingroups.Group{}, err
			}
		}
		name = &trimmed
	}
	var perms []access.Action
	if req.MemberPerms != nil {
		normalized, err := domaingroups.NormalizePerms(req.MemberPerms)
		if err != nil {
			return domaingroups.Group{}, apperr.ErrGroupPermsInvalid.WithCause(err)
		}
		perms = normalized
	}
	return s.db.UpdateGroup(ctx, groupID, name, perms)
}

// DeleteGroup 删除组。组内必须完全空闲：没有节点、没有进行中的上传/离线任务、
// 没有未撤销的分享，否则 409 group_not_empty。
func (s *Service) DeleteGroup(ctx context.Context, p access.Principal, groupID int64) error {
	if _, err := s.manageableGroup(ctx, p, groupID); err != nil {
		return err
	}
	busy, err := s.db.GroupHasContent(ctx, groupID)
	if err != nil {
		return err
	}
	if busy {
		return apperr.ErrGroupNotEmpty
	}
	return s.db.DeleteGroup(ctx, groupID)
}

// ListMembers 返回组成员。仅可管理该组的用户可调用。
func (s *Service) ListMembers(ctx context.Context, p access.Principal, groupID int64) ([]domaingroups.Member, error) {
	if _, err := s.manageableGroup(ctx, p, groupID); err != nil {
		return nil, err
	}
	return s.db.ListGroupMembers(ctx, groupID)
}

// AddMember 添加成员，已存在时按新角色更新（幂等）。role 为空时按 member 处理；
// 把最后一个 manager 降级为 member 返回 409 last_group_manager。返回值 created
// 表示是否新建了成员关系。
func (s *Service) AddMember(ctx context.Context, p access.Principal, groupID, userID int64, role domaingroups.Role) (domaingroups.Member, bool, error) {
	if _, err := s.manageableGroup(ctx, p, groupID); err != nil {
		return domaingroups.Member{}, false, err
	}
	if role == "" {
		role = domaingroups.RoleMember
	}
	if !role.Valid() {
		return domaingroups.Member{}, false, apperr.ErrBadRequest.WithReason(apperr.ReasonInvalidRole)
	}
	if _, err := s.db.UserByID(ctx, userID); err != nil {
		if apperr.CodeOf(err) == apperr.CodeNotFound {
			return domaingroups.Member{}, false, apperr.ErrNotFound
		}
		return domaingroups.Member{}, false, err
	}
	current, exists, err := s.db.GroupMemberRole(ctx, groupID, userID)
	if err != nil {
		return domaingroups.Member{}, false, err
	}
	if exists {
		if current == role {
			m, err := s.member(ctx, groupID, userID)
			return m, false, err
		}
		if current == domaingroups.RoleManager {
			if err := s.requireAnotherManager(ctx, groupID); err != nil {
				return domaingroups.Member{}, false, err
			}
		}
		if err := s.db.UpdateGroupMemberRole(ctx, groupID, userID, role); err != nil {
			return domaingroups.Member{}, false, err
		}
		m, err := s.member(ctx, groupID, userID)
		return m, false, err
	}
	if err := s.db.AddGroupMember(ctx, groupID, userID, role, s.now()); err != nil {
		return domaingroups.Member{}, false, err
	}
	m, err := s.member(ctx, groupID, userID)
	return m, true, err
}

// member 读取单个成员的展示信息。
func (s *Service) member(ctx context.Context, groupID, userID int64) (domaingroups.Member, error) {
	members, err := s.db.ListGroupMembers(ctx, groupID)
	if err != nil {
		return domaingroups.Member{}, err
	}
	for _, m := range members {
		if m.UserID == userID {
			return m, nil
		}
	}
	return domaingroups.Member{}, apperr.ErrInternal.WithCause(errors.New("group member vanished"))
}

// UpdateMemberRole 改成员角色；把最后一个 manager 降级为 member 返回 409
// last_group_manager。
func (s *Service) UpdateMemberRole(ctx context.Context, p access.Principal, groupID, userID int64, role domaingroups.Role) error {
	if _, err := s.manageableGroup(ctx, p, groupID); err != nil {
		return err
	}
	if !role.Valid() {
		return apperr.ErrBadRequest.WithReason(apperr.ReasonInvalidRole)
	}
	current, exists, err := s.db.GroupMemberRole(ctx, groupID, userID)
	if err != nil {
		return err
	}
	if !exists {
		return apperr.ErrNotFound
	}
	if current == role {
		return nil
	}
	if current == domaingroups.RoleManager && role != domaingroups.RoleManager {
		if err := s.requireAnotherManager(ctx, groupID); err != nil {
			return err
		}
	}
	return s.db.UpdateGroupMemberRole(ctx, groupID, userID, role)
}

// RemoveMember 移除成员；移除最后一个 manager 返回 409 last_group_manager。
func (s *Service) RemoveMember(ctx context.Context, p access.Principal, groupID, userID int64) error {
	if _, err := s.manageableGroup(ctx, p, groupID); err != nil {
		return err
	}
	current, exists, err := s.db.GroupMemberRole(ctx, groupID, userID)
	if err != nil {
		return err
	}
	if !exists {
		return apperr.ErrNotFound
	}
	if current == domaingroups.RoleManager {
		if err := s.requireAnotherManager(ctx, groupID); err != nil {
			return err
		}
	}
	return s.db.RemoveGroupMember(ctx, groupID, userID)
}

// requireAnotherManager 确认除当前成员外还有 manager，保证组始终可被管理。
func (s *Service) requireAnotherManager(ctx context.Context, groupID int64) error {
	n, err := s.db.CountGroupManagers(ctx, groupID)
	if err != nil {
		return err
	}
	if n <= 1 {
		return apperr.ErrLastGroupManager
	}
	return nil
}

// CanManageGroup 报告 principal 能否管理该组：系统 admin，或拥有建组能力且
// 是该组创建者。
func (s *Service) CanManageGroup(p access.Principal, g domaingroups.Group) bool {
	if p.Perms.Has(access.ActionAdminUsers) {
		return true
	}
	return p.CanCreateGroups && g.CreatedBy != nil && *g.CreatedBy == p.UserID
}

// Group 读取一个组；不存在或无权管理都映射为 ErrNotFound，避免暴露组存在性。
func (s *Service) Group(ctx context.Context, p access.Principal, groupID int64) (domaingroups.Group, error) {
	return s.manageableGroup(ctx, p, groupID)
}

// manageableGroup 读取组并校验管理权限，越权按 404 处理。
func (s *Service) manageableGroup(ctx context.Context, p access.Principal, groupID int64) (domaingroups.Group, error) {
	if p.UserID <= 0 || p.Anonymous || p.System {
		return domaingroups.Group{}, apperr.ErrNotFound
	}
	g, err := s.db.GroupByID(ctx, groupID)
	if err != nil {
		return domaingroups.Group{}, err
	}
	if !s.CanManageGroup(p, g) {
		return domaingroups.Group{}, apperr.ErrNotFound
	}
	return g, nil
}

// canCreate 报告调用者是否能创建组：系统 admin 或被授予建组能力。
func (s *Service) canCreate(p access.Principal) bool {
	if p.UserID <= 0 || p.Anonymous || p.System {
		return false
	}
	return p.CanCreateGroups || p.Perms.Has(access.ActionAdminUsers)
}
