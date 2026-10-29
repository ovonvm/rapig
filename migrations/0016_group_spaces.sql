-- 0016_group_spaces: P1 组共享空间。
--
-- 组空间是多个用户共享的一棵文件树。每个组恰好有一个根节点（nodes.group_id
-- 非空、parent_id 为空），组成员在 group_members 里持有角色，角色到动作的映射
-- 由 access.Principal.GroupRoles 在策略层完成：
--
--   * manager 恒定拥有六个文件动作（read/create/upload/write/delete/share），
--     与 member_perms 无关；
--   * member 只拥有 groups.member_perms 里的动作，该列存 JSON 动作数组，必须
--     包含 "read"，且不允许出现 admin.* 管理动作；
--   * 组空间没有回收站：删除组节点就是永久删除，所以不需要 trashed 相关约束；
--   * 组没有独立配额：节点 owner_id 仍然记上传者，配额与清算都按 owner 记账。
--
-- 个人树与组树完全隔离。原来的 nodes_one_root(owner_id) 只约束个人根，改用两
-- 条部分唯一索引后，个人根（group_id IS NULL）与组根（group_id IS NOT NULL）
-- 各自唯一，且组根不会和任何人的个人根冲突。
--
-- 建组能力是 users.can_create_groups（默认关闭）。系统 admin 仍可通过
-- admin.users 权限建组并管理全部组，但不会因此获得组内文件的旁路。

-- ---------------------------------------------------------------------------
-- groups / group_members
-- ---------------------------------------------------------------------------
CREATE TABLE groups (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    name          TEXT    NOT NULL,
    name_folded   TEXT    NOT NULL UNIQUE,
    created_by    INTEGER REFERENCES users(id) ON DELETE SET NULL,
    member_perms  TEXT    NOT NULL DEFAULT '["read"]',
    created_at    INTEGER NOT NULL
);

CREATE TABLE group_members (
    group_id   INTEGER NOT NULL REFERENCES groups(id) ON DELETE CASCADE,
    user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    role       TEXT    NOT NULL CHECK (role IN ('manager', 'member')),
    created_at INTEGER NOT NULL,
    PRIMARY KEY (group_id, user_id)
);

-- 登录后要一次取出用户的全部组角色，按 user 反查。
CREATE INDEX group_members_user ON group_members(user_id);

-- ---------------------------------------------------------------------------
-- 建组能力
-- ---------------------------------------------------------------------------
ALTER TABLE users ADD COLUMN can_create_groups INTEGER NOT NULL DEFAULT 0
    CHECK (can_create_groups IN (0,1));

-- ---------------------------------------------------------------------------
-- nodes.group_id
--
-- 只加列，不给 uploads/transfers 加列：上传与离线任务在完成时从父节点继承
-- group_id（父节点不会在任务进行中改变归属）。
-- ---------------------------------------------------------------------------
ALTER TABLE nodes ADD COLUMN group_id INTEGER REFERENCES groups(id);

-- ---------------------------------------------------------------------------
-- 根唯一性：个人根与组根各一条部分唯一索引。
-- ---------------------------------------------------------------------------
DROP INDEX nodes_one_root;

CREATE UNIQUE INDEX nodes_one_root_personal ON nodes(owner_id)
    WHERE parent_id IS NULL AND group_id IS NULL;

CREATE UNIQUE INDEX nodes_one_root_group ON nodes(group_id)
    WHERE parent_id IS NULL AND group_id IS NOT NULL;

-- 组内列目录、组内搜索与组删除的空检查都按 (group_id, parent_id) 过滤。
CREATE INDEX nodes_group_parent ON nodes(group_id, parent_id);
