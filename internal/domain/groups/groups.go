// Package groups 定义组共享空间的领域模型：组、组成员角色与成员权限集合。
//
// 它只依赖 access，不含存储与 HTTP 细节，这样策略层（access）、仓储
// （infra/sql）和应用服务（app/groups）可以共用同一套角色与权限语义。
//
// 角色语义：
//
//   - manager：组管理者，恒定拥有六个文件动作，与组的 member_perms 无关；
//   - member：普通成员，只拥有 member_perms 里列出的动作，且必须包含 read。
//
// 成员权限里不允许出现 admin.* 管理动作：系统管理能力永远不能通过组下放。
package groups

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"rapig/internal/domain/access"
	"rapig/internal/platform/apperr"
)

// Role 是组成员角色，取值与 group_members.role 的 CHECK 约束一致。
// 它复用 access.GroupRole，策略层判组资源时直接看同一个类型。
type Role = access.GroupRole

const (
	// RoleManager 是组管理者：可执行全部文件动作，并管理组成员。
	RoleManager = access.GroupRoleManager
	// RoleMember 是普通成员：动作受组的 member_perms 限制。
	RoleMember = access.GroupRoleMember
)

// ParseRole 把外部输入解析成角色。
func ParseRole(s string) (Role, bool) {
	r := Role(strings.TrimSpace(s))
	if !r.Valid() {
		return "", false
	}
	return r, true
}

// MaxNameLength 是组名的最大 rune 数。
const MaxNameLength = 64

// ValidateName 校验并归一化组名：去掉首尾空白后必须为 1..64 个字符，且不含
// 控制字符。组名是展示名，因此允许空格与中文，但排除换行、制表等会破坏日志与
// 响应体的字符。
func ValidateName(name string) (string, error) {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return "", apperr.ErrInvalidName
	}
	if utf8.RuneCountInString(trimmed) > MaxNameLength {
		return "", apperr.ErrInvalidName
	}
	for _, r := range trimmed {
		if r < 0x20 || r == 0x7f {
			return "", apperr.ErrInvalidName
		}
	}
	return trimmed, nil
}

// FoldName 是组名的折叠规则：与用户名/文件名一致，只做大小写折叠，
// 用于 groups.name_folded 的唯一约束与重名判断。
func FoldName(name string) string { return strings.ToLower(strings.TrimSpace(name)) }

// FileActions 是组内可授予的六个文件动作，顺序即规范输出顺序。
var FileActions = []access.Action{
	access.ActionRead,
	access.ActionCreate,
	access.ActionUpload,
	access.ActionWrite,
	access.ActionDelete,
	access.ActionShare,
}

// ManagerPerms 返回 manager 的固定权限集合（六个文件动作）。
func ManagerPerms() access.PermSet { return access.NewPermSet(FileActions...) }

// DefaultMemberPerms 是新组的默认成员权限：只读。
func DefaultMemberPerms() []access.Action { return []access.Action{access.ActionRead} }

// FileAction 报告 a 是否属于组内可授予的六个文件动作。
func FileAction(a access.Action) bool {
	for _, known := range FileActions {
		if a == known {
			return true
		}
	}
	return false
}

// NormalizePerms 校验并规范化成员权限：只允许六个文件动作、必须包含 read、
// 去重后按规范顺序返回。空输入或缺失 read 都返回错误。
func NormalizePerms(actions []access.Action) ([]access.Action, error) {
	if len(actions) == 0 {
		return nil, fmt.Errorf("groups: member perms must not be empty")
	}
	seen := make(map[access.Action]bool, len(actions))
	for _, a := range actions {
		if !FileAction(a) {
			return nil, fmt.Errorf("groups: action %q is not a group file action", a)
		}
		seen[a] = true
	}
	if !seen[access.ActionRead] {
		return nil, fmt.Errorf("groups: member perms must include read")
	}
	out := make([]access.Action, 0, len(seen))
	for _, a := range FileActions {
		if seen[a] {
			out = append(out, a)
		}
	}
	return out, nil
}

// EncodePerms 把动作集合编码成 groups.member_perms 里的 JSON 文本。
func EncodePerms(actions []access.Action) (string, error) {
	normalized, err := NormalizePerms(actions)
	if err != nil {
		return "", err
	}
	codes := make([]string, 0, len(normalized))
	for _, a := range normalized {
		codes = append(codes, string(a))
	}
	raw, err := json.Marshal(codes)
	if err != nil {
		return "", fmt.Errorf("groups: encode member perms: %w", err)
	}
	return string(raw), nil
}

// DecodePerms 解析 groups.member_perms 列。历史或手工写入的非法值返回错误，
// 由调用方决定是降级为只读还是报 500。
func DecodePerms(text string) ([]access.Action, error) {
	if strings.TrimSpace(text) == "" {
		return DefaultMemberPerms(), nil
	}
	var codes []string
	if err := json.Unmarshal([]byte(text), &codes); err != nil {
		return nil, fmt.Errorf("groups: decode member perms: %w", err)
	}
	actions := make([]access.Action, 0, len(codes))
	for _, code := range codes {
		a, ok := access.ParseAction(code)
		if !ok || !FileAction(a) {
			return nil, fmt.Errorf("groups: unknown member action %q", code)
		}
		actions = append(actions, a)
	}
	return NormalizePerms(actions)
}

// PermSetFor 返回某角色在给定 member_perms 下的实际权限集合。
func PermSetFor(role Role, memberPerms []access.Action) access.PermSet {
	if role == RoleManager {
		return ManagerPerms()
	}
	return access.NewPermSet(memberPerms...)
}

// Group 是一个共享空间。MemberPerms 已解码为动作列表。
type Group struct {
	ID          int64
	Name        string
	NameFolded  string
	CreatedBy   *int64
	MemberPerms []access.Action
	CreatedAt   time.Time
	// RootID 是组空间根节点 id；/groups 列表与创建响应靠它进入组空间。
	RootID int64
	// Role 是调用者在组里的角色，只在“我的组”视图有值；管理视图为空。
	Role Role
	// MemberCount / NodeCount 只用于管理列表展示；NodeCount 不含组根。
	MemberCount int
	NodeCount   int
}

// Member 是组成员视图（带用户名，便于管理接口直接返回）。
type Member struct {
	GroupID     int64
	UserID      int64
	Username    string
	DisplayName string
	Role        Role
	CreatedAt   time.Time
}

// SortMembers 按角色（manager 在前）再按 user id 排序，保证接口输出稳定。
func SortMembers(items []Member) {
	sort.SliceStable(items, func(i, j int) bool {
		if (items[i].Role == RoleManager) != (items[j].Role == RoleManager) {
			return items[i].Role == RoleManager
		}
		return items[i].UserID < items[j].UserID
	})
}
