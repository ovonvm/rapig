-- M0 identity schema: roles, permissions, users, sessions, settings, audit.
-- Timestamps are unix seconds (INTEGER) throughout rapig.

CREATE TABLE roles (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    code        TEXT    NOT NULL UNIQUE,
    name        TEXT    NOT NULL,
    description TEXT    NOT NULL DEFAULT '',
    created_at  INTEGER NOT NULL
);

CREATE TABLE permissions (
    id   INTEGER PRIMARY KEY AUTOINCREMENT,
    code TEXT    NOT NULL UNIQUE,
    name TEXT    NOT NULL
);

CREATE TABLE role_permissions (
    role_id       INTEGER NOT NULL REFERENCES roles(id) ON DELETE CASCADE,
    permission_id INTEGER NOT NULL REFERENCES permissions(id) ON DELETE CASCADE,
    PRIMARY KEY (role_id, permission_id)
);

CREATE TABLE users (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    username        TEXT    NOT NULL,
    username_folded TEXT    NOT NULL UNIQUE,
    display_name    TEXT    NOT NULL DEFAULT '',
    password_hash   TEXT    NOT NULL,
    role_id         INTEGER NOT NULL REFERENCES roles(id),
    status          TEXT    NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'disabled')),
    used_bytes      INTEGER NOT NULL DEFAULT 0 CHECK (used_bytes >= 0),
    quota_bytes     INTEGER NOT NULL DEFAULT 0 CHECK (quota_bytes >= 0),
    created_at      INTEGER NOT NULL,
    updated_at      INTEGER NOT NULL,
    last_login_at   INTEGER
);
CREATE INDEX users_role ON users(role_id);

-- sessions.id is sha256hex(session token); the raw token only lives in the
-- client cookie. csrf_hash is sha256hex(csrf token) bound to the session.
CREATE TABLE sessions (
    id         TEXT    PRIMARY KEY,
    user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    csrf_hash  TEXT    NOT NULL,
    ip         TEXT    NOT NULL DEFAULT '',
    user_agent TEXT    NOT NULL DEFAULT '',
    created_at INTEGER NOT NULL,
    expires_at INTEGER NOT NULL
);
CREATE INDEX sessions_user ON sessions(user_id);
CREATE INDEX sessions_expires ON sessions(expires_at);

CREATE TABLE settings (
    key        TEXT    PRIMARY KEY,
    value      TEXT    NOT NULL,
    updated_at INTEGER NOT NULL
);

CREATE TABLE audit (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    at          INTEGER NOT NULL,
    actor_id    INTEGER,
    actor_name  TEXT    NOT NULL DEFAULT '',
    action      TEXT    NOT NULL,
    target_type TEXT    NOT NULL DEFAULT '',
    target_id   INTEGER NOT NULL DEFAULT 0,
    result      TEXT    NOT NULL DEFAULT '',
    ip          TEXT    NOT NULL DEFAULT '',
    user_agent  TEXT    NOT NULL DEFAULT '',
    request_id  TEXT    NOT NULL DEFAULT '',
    detail      TEXT    NOT NULL DEFAULT ''
);
CREATE INDEX audit_at ON audit(at);
CREATE INDEX audit_actor ON audit(actor_id, at);

INSERT INTO roles (code, name, description, created_at) VALUES
    ('guest', '游客', '仅可访问被分享的内容（M0 无分享）', unixepoch()),
    ('user',  '用户', '管理自己的文件', unixepoch()),
    ('admin', '管理员', '管理用户与站点设置', unixepoch());

INSERT INTO permissions (code, name) VALUES
    ('read',              '读取'),
    ('write',             '写入'),
    ('create',            '新建'),
    ('delete',            '删除'),
    ('share',             '分享'),
    ('upload',            '上传'),
    ('admin.users',       '用户管理'),
    ('admin.settings',    '站点设置'),
    ('admin.storages',    '存储策略'),
    ('admin.audit',       '审计日志'),
    ('admin.breakglass',  '越权读取（需二次确认）');

-- Regular users get every non-admin permission.
INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id FROM roles r, permissions p
WHERE r.code = 'user' AND p.code NOT LIKE 'admin.%';

-- Admins get everything (including admin.breakglass, which still requires an
-- explicit M1 confirmation flow; M0 always refuses it).
INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id FROM roles r, permissions p WHERE r.code = 'admin';
