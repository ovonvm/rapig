-- M4.2: offline HTTP(S) downloads. A transfer is a durable job row; the
-- worker streams the source to the object store and then creates the node.
CREATE TABLE IF NOT EXISTS transfers (
    id             TEXT PRIMARY KEY,
    owner_id       INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    parent_id      INTEGER NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
    name           TEXT NOT NULL,
    name_folded    TEXT NOT NULL,
    source_url     TEXT NOT NULL,
    size           INTEGER NOT NULL DEFAULT 0,
    received_bytes INTEGER NOT NULL DEFAULT 0,
    state          TEXT NOT NULL DEFAULT 'pending'
                   CHECK (state IN ('pending','running','paused','completed','failed','canceled')),
    error          TEXT NOT NULL DEFAULT '',
    node_id        INTEGER REFERENCES nodes(id) ON DELETE SET NULL,
    created_at     INTEGER NOT NULL,
    updated_at     INTEGER NOT NULL,
    started_at     INTEGER,
    finished_at    INTEGER
);

CREATE INDEX IF NOT EXISTS transfers_owner_created ON transfers(owner_id, created_at DESC);
CREATE INDEX IF NOT EXISTS transfers_state ON transfers(state, created_at);
