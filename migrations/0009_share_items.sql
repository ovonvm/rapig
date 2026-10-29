-- 0009: merged multi-file shares (M3.2.1).
--
-- A share can now cover several nodes at once (one token, one password, one
-- expiry, one download budget). `shares.node_id` stays as the primary/display
-- node; the full member set lives in `share_items`.
--
-- SQLite cannot alter a CHECK constraint, so `shares` is rebuilt to allow
-- kind = 'multi'. The table is rebuilt before `share_items` exists; no other
-- table references `shares` yet.

CREATE TABLE shares_new (
    id             TEXT PRIMARY KEY,
    token          TEXT NOT NULL UNIQUE,
    owner_id       INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    node_id        INTEGER NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
    kind           TEXT NOT NULL CHECK (kind IN ('file', 'dir', 'multi')),
    password_hash  TEXT NOT NULL DEFAULT '',
    expires_at     INTEGER,
    max_downloads  INTEGER NOT NULL DEFAULT 0 CHECK (max_downloads >= 0),
    download_count INTEGER NOT NULL DEFAULT 0 CHECK (download_count >= 0),
    visits_count   INTEGER NOT NULL DEFAULT 0 CHECK (visits_count >= 0),
    last_access_at INTEGER,
    created_at     INTEGER NOT NULL,
    revoked_at     INTEGER
);

INSERT INTO shares_new
    (id, token, owner_id, node_id, kind, password_hash, expires_at,
     max_downloads, download_count, visits_count, last_access_at,
     created_at, revoked_at)
SELECT id, token, owner_id, node_id, kind, password_hash, expires_at,
       max_downloads, download_count, visits_count, last_access_at,
       created_at, revoked_at
FROM shares;

DROP TABLE shares;

ALTER TABLE shares_new RENAME TO shares;

CREATE INDEX shares_owner ON shares(owner_id, created_at DESC);
CREATE INDEX shares_node ON shares(node_id);
CREATE INDEX shares_token ON shares(token);

CREATE TABLE share_items (
    share_id TEXT NOT NULL REFERENCES shares(id) ON DELETE CASCADE,
    node_id  INTEGER NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
    PRIMARY KEY (share_id, node_id)
);

CREATE INDEX share_items_node ON share_items(node_id);

INSERT OR IGNORE INTO share_items (share_id, node_id)
    SELECT id, node_id FROM shares WHERE node_id IS NOT NULL;
