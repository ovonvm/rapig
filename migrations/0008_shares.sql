-- 0008_shares.sql: public share links (M3.2).
--
-- A share points at one active node (file or directory). The public URL uses
-- `token`; `id` is only used by the owner for revoke/list. Password hashes are
-- argon2id PHC strings (empty = no password). `expires_at` NULL means never.
-- `max_downloads` 0 means unlimited. Counters are only informational except
-- download_count, which is enforced atomically.

CREATE TABLE shares (
    id             TEXT PRIMARY KEY,
    token          TEXT NOT NULL UNIQUE,
    owner_id       INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    node_id        INTEGER NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
    kind           TEXT NOT NULL CHECK (kind IN ('file', 'dir')),
    password_hash  TEXT NOT NULL DEFAULT '',
    expires_at     INTEGER,
    max_downloads  INTEGER NOT NULL DEFAULT 0 CHECK (max_downloads >= 0),
    download_count INTEGER NOT NULL DEFAULT 0 CHECK (download_count >= 0),
    visits_count   INTEGER NOT NULL DEFAULT 0 CHECK (visits_count >= 0),
    last_access_at INTEGER,
    created_at     INTEGER NOT NULL,
    revoked_at     INTEGER
);

CREATE INDEX shares_owner ON shares(owner_id, created_at DESC);
CREATE INDEX shares_node ON shares(node_id);
CREATE INDEX shares_token ON shares(token);
