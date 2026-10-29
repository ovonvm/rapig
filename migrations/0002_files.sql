-- M0 file tree schema: storage policies, content addressed blobs, nodes.
--
-- Nodes carry the user visible tree. A root node is one row per owner with
-- parent_id NULL and name ''. path is a relative display path without the
-- owner prefix, e.g. '' for root and 'docs/report.txt' below it; it is derived
-- and updated in the same transaction as the tree change.

CREATE TABLE storage_policies (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    name       TEXT    NOT NULL UNIQUE,
    type       TEXT    NOT NULL DEFAULT 'local' CHECK (type IN ('local')),
    root_path  TEXT    NOT NULL,
    used_bytes INTEGER NOT NULL DEFAULT 0 CHECK (used_bytes >= 0),
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
);

CREATE TABLE blobs (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    sha256     TEXT    NOT NULL UNIQUE,
    size       INTEGER NOT NULL CHECK (size >= 0),
    refcount   INTEGER NOT NULL DEFAULT 0 CHECK (refcount >= 0),
    created_at INTEGER NOT NULL
);

CREATE TABLE nodes (
    id                INTEGER PRIMARY KEY AUTOINCREMENT,
    owner_id          INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    parent_id         INTEGER REFERENCES nodes(id) ON DELETE CASCADE,
    kind              TEXT    NOT NULL CHECK (kind IN ('dir', 'file')),
    name              TEXT    NOT NULL,
    name_folded       TEXT    NOT NULL,
    path              TEXT    NOT NULL DEFAULT '',
    blob_id           INTEGER REFERENCES blobs(id),
    size              INTEGER NOT NULL DEFAULT 0 CHECK (size >= 0),
    storage_policy_id INTEGER REFERENCES storage_policies(id),
    state             TEXT    NOT NULL DEFAULT 'active' CHECK (state IN ('active', 'trashed', 'purged')),
    created_at        INTEGER NOT NULL,
    updated_at        INTEGER NOT NULL
);

-- Exactly one root (parent_id IS NULL) per owner.
CREATE UNIQUE INDEX nodes_one_root ON nodes(owner_id) WHERE parent_id IS NULL;

-- Active sibling names are unique within a directory, case-insensitively.
CREATE UNIQUE INDEX nodes_active_name ON nodes(parent_id, name_folded)
    WHERE state = 'active' AND parent_id IS NOT NULL;

CREATE INDEX nodes_owner_parent ON nodes(owner_id, parent_id);
CREATE INDEX nodes_blob ON nodes(blob_id);
