package sqlstore

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"rapig/internal/domain/access"
	"rapig/internal/domain/groups"
	"rapig/internal/platform/apperr"
)

// groupColumns 是 groups 表的固定投影，顺序与 scanGroup 一致。g 是 groups 的
// 别名；root_id 与两个计数用子查询取，避免每个调用方各拼一次 JOIN。
const groupColumns = `g.id, g.name, g.name_folded, g.created_by, g.member_perms, g.created_at,
	(SELECT id FROM nodes WHERE group_id = g.id AND parent_id IS NULL) AS root_id,
	(SELECT COUNT(*) FROM group_members gm WHERE gm.group_id = g.id) AS member_count,
	(SELECT COUNT(*) FROM nodes n WHERE n.group_id = g.id AND n.parent_id IS NOT NULL AND n.state = 'active') AS node_count`

// memberColumns 是组成员视图的投影：成员行加上用户展示字段。
const memberColumns = `m.group_id, m.user_id, u.username, u.display_name, m.role, m.created_at`

// CreateGroup 在同一个事务里插入组、组根节点和首位 manager 成员。
// 调用方负责校验名称、重名与权限；这里只保证三行一起出现或一起不存在。
func (d *DB) CreateGroup(ctx context.Context, g groups.Group, managerID int64) (groups.Group, int64, error) {
	perms, err := groups.EncodePerms(g.MemberPerms)
	if err != nil {
		return groups.Group{}, 0, err
	}
	var id, rootID int64
	err = d.WithTx(ctx, func(tx *sql.Tx) error {
		now := g.CreatedAt.Unix()
		res, err := tx.ExecContext(ctx,
			`INSERT INTO groups (name, name_folded, created_by, member_perms, created_at)
			 VALUES (?, ?, ?, ?, ?)`,
			g.Name, g.NameFolded, g.CreatedBy, perms, now)
		if err != nil {
			if isUniqueViolation(err) {
				return apperr.ErrNameConflict
			}
			return fmt.Errorf("sqlstore: insert group: %w", err)
		}
		id, err = res.LastInsertId()
		if err != nil {
			return fmt.Errorf("sqlstore: group id: %w", err)
		}
		// 组根：parent NULL + group_id 命中 nodes_one_root_group，owner 记创建者，
		// 让审计与配额归属有据可查。根节点不收费、不进回收站。
		res, err = tx.ExecContext(ctx,
			`INSERT INTO nodes (owner_id, parent_id, kind, name, name_folded, path,
				group_id, storage_policy_id, state, created_at, updated_at)
			 VALUES (?, NULL, 'dir', '', '', '', ?, 
				(SELECT id FROM storage_policies WHERE is_default = 1), 'active', ?, ?)`,
			g.CreatedBy, id, now, now)
		if err != nil {
			return fmt.Errorf("sqlstore: insert group root: %w", err)
		}
		rootID, err = res.LastInsertId()
		if err != nil {
			return fmt.Errorf("sqlstore: group root id: %w", err)
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO group_members (group_id, user_id, role, created_at)
			 VALUES (?, ?, 'manager', ?)`, id, managerID, now); err != nil {
			if isForeignKeyViolation(err) {
				return apperr.ErrNotFound
			}
			return fmt.Errorf("sqlstore: insert group manager: %w", err)
		}
		return nil
	})
	if err != nil {
		return groups.Group{}, 0, err
	}
	out, err := d.GroupByID(ctx, id)
	if err != nil {
		return groups.Group{}, 0, err
	}
	return out, rootID, nil
}

// GroupByID 读取一个组；不存在时返回 ErrNotFound。
func (d *DB) GroupByID(ctx context.Context, id int64) (groups.Group, error) {
	row := d.QueryRowContext(ctx, `SELECT `+groupColumns+`, '' AS role FROM groups g WHERE g.id = ?`, id)
	out, err := scanGroup(row)
	if isNoRows(err) {
		return groups.Group{}, apperr.ErrNotFound
	}
	if err != nil {
		return groups.Group{}, fmt.Errorf("sqlstore: group by id: %w", err)
	}
	return out, nil
}

// GroupByNameFolded 按折叠名查组，用于创建/改名前的重名判断。
func (d *DB) GroupByNameFolded(ctx context.Context, folded string) (groups.Group, error) {
	row := d.QueryRowContext(ctx, `SELECT `+groupColumns+`, '' AS role FROM groups g WHERE g.name_folded = ?`, folded)
	out, err := scanGroup(row)
	if isNoRows(err) {
		return groups.Group{}, apperr.ErrNotFound
	}
	if err != nil {
		return groups.Group{}, fmt.Errorf("sqlstore: group by name: %w", err)
	}
	return out, nil
}

// UpdateGroup 改名和/或改 member_perms；nil 表示保持原值。
func (d *DB) UpdateGroup(ctx context.Context, id int64, name *string, perms []access.Action) (groups.Group, error) {
	sets := make([]string, 0, 2)
	args := make([]any, 0, 4)
	if name != nil {
		sets = append(sets, "name = ?", "name_folded = ?")
		args = append(args, *name, foldGroupName(*name))
	}
	if perms != nil {
		encoded, err := groups.EncodePerms(perms)
		if err != nil {
			return groups.Group{}, apperr.ErrGroupPermsInvalid
		}
		sets = append(sets, "member_perms = ?")
		args = append(args, encoded)
	}
	if len(sets) == 0 {
		return d.GroupByID(ctx, id)
	}
	args = append(args, id)
	res, err := d.ExecContext(ctx,
		`UPDATE groups SET `+strings.Join(sets, ", ")+` WHERE id = ?`, args...)
	if err != nil {
		if isUniqueViolation(err) {
			return groups.Group{}, apperr.ErrNameConflict
		}
		return groups.Group{}, fmt.Errorf("sqlstore: update group: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return groups.Group{}, apperr.ErrNotFound
	}
	return d.GroupByID(ctx, id)
}

// DeleteGroup 删除组本身及其根节点。内容清空由服务层先行检查，这里不再判定；
// 组内节点行由 nodes.group_id 的外键约束保护，误删会被数据库拒绝。
func (d *DB) DeleteGroup(ctx context.Context, id int64) error {
	return d.WithTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `DELETE FROM nodes WHERE group_id = ?`, id); err != nil {
			return fmt.Errorf("sqlstore: delete group root: %w", err)
		}
		res, err := tx.ExecContext(ctx, `DELETE FROM groups WHERE id = ?`, id)
		if err != nil {
			return fmt.Errorf("sqlstore: delete group: %w", err)
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return apperr.ErrNotFound
		}
		return nil
	})
}

// ListGroups 返回全部组（系统 admin 视图）。
func (d *DB) ListGroups(ctx context.Context) ([]groups.Group, error) {
	return d.queryGroups(ctx, `SELECT `+groupColumns+`, '' AS role FROM groups g ORDER BY g.name_folded, g.id`)
}

// ListGroupsCreatedBy 返回某用户创建的组（被指派的建组者视图）。
func (d *DB) ListGroupsCreatedBy(ctx context.Context, userID int64) ([]groups.Group, error) {
	return d.queryGroups(ctx,
		`SELECT `+groupColumns+`, '' AS role FROM groups g WHERE g.created_by = ? ORDER BY g.name_folded, g.id`, userID)
}

// ListGroupsForUser 返回用户加入的组（普通用户的 /groups 视图）。
func (d *DB) ListGroupsForUser(ctx context.Context, userID int64) ([]groups.Group, error) {
	return d.queryGroups(ctx,
		`SELECT `+groupColumns+`, m.role FROM groups g
		 JOIN group_members m ON m.group_id = g.id
		 WHERE m.user_id = ? ORDER BY g.name_folded, g.id`, userID)
}

// GroupRolesForUser 返回用户的组成员身份表，键为组 id。登录认证时一次性取出，
// 供 access.Principal.GroupRoles 使用；manager 的权限集合恒为六个文件动作。
func (d *DB) GroupRolesForUser(ctx context.Context, userID int64) (map[int64]access.GroupRoleInfo, error) {
	rows, err := d.QueryContext(ctx,
		`SELECT g.id, g.member_perms, m.role FROM group_members m
		 JOIN groups g ON g.id = m.group_id WHERE m.user_id = ?`, userID)
	if err != nil {
		return nil, fmt.Errorf("sqlstore: group roles for user: %w", err)
	}
	defer rows.Close()
	out := make(map[int64]access.GroupRoleInfo)
	for rows.Next() {
		var (
			groupID int64
			perms   string
			role    string
		)
		if err := rows.Scan(&groupID, &perms, &role); err != nil {
			return nil, fmt.Errorf("sqlstore: scan group role: %w", err)
		}
		decoded, err := groups.DecodePerms(perms)
		if err != nil {
			return nil, err
		}
		out[groupID] = access.GroupRoleInfo{
			Role:  access.GroupRole(role),
			Perms: groups.PermSetFor(access.GroupRole(role), decoded),
		}
	}
	return out, rows.Err()
}

// ListGroupMembers 返回组成员，manager 在前、同角色按 user id 升序。
func (d *DB) ListGroupMembers(ctx context.Context, groupID int64) ([]groups.Member, error) {
	rows, err := d.QueryContext(ctx,
		`SELECT `+memberColumns+` FROM group_members m JOIN users u ON u.id = m.user_id
		 WHERE m.group_id = ?
		 ORDER BY CASE m.role WHEN 'manager' THEN 0 ELSE 1 END, m.user_id`, groupID)
	if err != nil {
		return nil, fmt.Errorf("sqlstore: list group members: %w", err)
	}
	defer rows.Close()
	out := make([]groups.Member, 0, 8)
	for rows.Next() {
		m, err := scanMember(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("sqlstore: list group members: %w", err)
	}
	return out, nil
}

// GroupMemberRole 查单个成员的角色；不存在返回 false。
func (d *DB) GroupMemberRole(ctx context.Context, groupID, userID int64) (groups.Role, bool, error) {
	var role string
	err := d.QueryRowContext(ctx,
		`SELECT role FROM group_members WHERE group_id = ? AND user_id = ?`,
		groupID, userID).Scan(&role)
	if isNoRows(err) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("sqlstore: group member role: %w", err)
	}
	return groups.Role(role), true, nil
}

// AddGroupMember 插入成员；已是成员时返回 ErrNameConflict 语义的冲突，
// 由服务层转成 409（调用方应先查 GroupMemberRole）。
func (d *DB) AddGroupMember(ctx context.Context, groupID, userID int64, role groups.Role, now time.Time) error {
	_, err := d.ExecContext(ctx,
		`INSERT INTO group_members (group_id, user_id, role, created_at) VALUES (?, ?, ?, ?)`,
		groupID, userID, string(role), now.Unix())
	if err != nil {
		if isUniqueViolation(err) {
			return apperr.ErrInvalidState.WithReason(apperr.ReasonInvalidValue).
				WithCause(fmt.Errorf("user %d is already a member of group %d", userID, groupID))
		}
		if isForeignKeyViolation(err) {
			return apperr.ErrNotFound
		}
		return fmt.Errorf("sqlstore: add group member: %w", err)
	}
	return nil
}

// UpdateGroupMemberRole 改单个成员角色。
func (d *DB) UpdateGroupMemberRole(ctx context.Context, groupID, userID int64, role groups.Role) error {
	res, err := d.ExecContext(ctx,
		`UPDATE group_members SET role = ? WHERE group_id = ? AND user_id = ?`,
		string(role), groupID, userID)
	if err != nil {
		return fmt.Errorf("sqlstore: update group member role: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return apperr.ErrNotFound
	}
	return nil
}

// RemoveGroupMember 删除成员；最后一个 manager 的保护由服务层在同一判断里完成，
// 仓储只做行删除。
func (d *DB) RemoveGroupMember(ctx context.Context, groupID, userID int64) error {
	res, err := d.ExecContext(ctx,
		`DELETE FROM group_members WHERE group_id = ? AND user_id = ?`, groupID, userID)
	if err != nil {
		return fmt.Errorf("sqlstore: remove group member: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return apperr.ErrNotFound
	}
	return nil
}

// CountGroupManagers 数一个组的 manager 数量。
func (d *DB) CountGroupManagers(ctx context.Context, groupID int64) (int, error) {
	var n int
	if err := d.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM group_members WHERE group_id = ? AND role = 'manager'`,
		groupID).Scan(&n); err != nil {
		return 0, fmt.Errorf("sqlstore: count group managers: %w", err)
	}
	return n, nil
}

// GroupHasContent 报告组内是否还有节点、进行中的上传/离线任务或未撤销的分享。
// 组根节点不算内容：它跟随组创建与删除。
func (d *DB) GroupHasContent(ctx context.Context, groupID int64) (bool, error) {
	const q = `SELECT
		(SELECT COUNT(*) FROM nodes WHERE group_id = ? AND parent_id IS NOT NULL)
		+ (SELECT COUNT(*) FROM uploads u JOIN nodes p ON p.id = u.parent_id
			WHERE p.group_id = ? AND u.state = 'open')
		+ (SELECT COUNT(*) FROM transfers t JOIN nodes p ON p.id = t.parent_id
			WHERE p.group_id = ? AND t.state IN ('pending','running','paused'))
		+ (SELECT COUNT(*) FROM shares s JOIN nodes n ON n.id = s.node_id
			WHERE n.group_id = ? AND s.revoked_at IS NULL)`
	var n int
	if err := d.QueryRowContext(ctx, q, groupID, groupID, groupID, groupID).Scan(&n); err != nil {
		return false, fmt.Errorf("sqlstore: group content: %w", err)
	}
	return n > 0, nil
}

// queryGroups 执行一个返回 groupColumns 的查询并解码成员权限。
func (d *DB) queryGroups(ctx context.Context, q string, args ...any) ([]groups.Group, error) {
	rows, err := d.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("sqlstore: list groups: %w", err)
	}
	defer rows.Close()
	out := make([]groups.Group, 0, 8)
	for rows.Next() {
		g, err := scanGroup(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("sqlstore: list groups: %w", err)
	}
	return out, nil
}

// scanGroup 按 groupColumns 的顺序扫描一行组记录。
func scanGroup(row interface{ Scan(...any) error }) (groups.Group, error) {
	var (
		g        groups.Group
		created  sql.NullInt64
		perms    string
		createdT int64
		rootID   sql.NullInt64
		role     string
	)
	if err := row.Scan(&g.ID, &g.Name, &g.NameFolded, &created, &perms, &createdT,
		&rootID, &g.MemberCount, &g.NodeCount, &role); err != nil {
		return groups.Group{}, err
	}
	if rootID.Valid {
		g.RootID = rootID.Int64
	}
	g.Role = groups.Role(role)
	if created.Valid {
		g.CreatedBy = &created.Int64
	}
	decoded, err := groups.DecodePerms(perms)
	if err != nil {
		return groups.Group{}, err
	}
	g.MemberPerms = decoded
	g.CreatedAt = unixUTC(createdT)
	return g, nil
}

// scanMember 按 memberColumns 的顺序扫描一行成员记录。
func scanMember(row interface{ Scan(...any) error }) (groups.Member, error) {
	var (
		m       groups.Member
		role    string
		created int64
	)
	if err := row.Scan(&m.GroupID, &m.UserID, &m.Username, &m.DisplayName, &role, &created); err != nil {
		return groups.Member{}, fmt.Errorf("sqlstore: scan group member: %w", err)
	}
	m.Role = groups.Role(role)
	m.CreatedAt = unixUTC(created)
	return m, nil
}

// foldGroupName 是组名的折叠规则：与文件/用户标识一致，只做大小写折叠。
func foldGroupName(name string) string { return groups.FoldName(name) }
