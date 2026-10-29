-- M5.3: multiple local storage policies.
--
-- Until M5.2b a deployment had exactly one local root (storage_policies had a
-- single row and blobs were keyed by sha256 alone). M5.3 lets an admin register
-- several roots and lets a node pick one. The invariants are:
--
--   * every policy row knows whether it is enabled and which one is default;
--   * a blob is unique per (storage_policy_id, sha256) so the same content can
--     live in two roots without sharing a row (dedup stays per root);
--   * every node carries a storage policy (the tree inherits it downwards);
--   * an upload and a transfer remember the policy they were started for.
--
-- migrator: foreign-keys-off
--
-- Rebuilding blobs means dropping a table that nodes.blob_id still references.
-- The migrator runs the file on a connection with foreign key enforcement
-- switched off (SQLite only honours PRAGMA foreign_keys outside a transaction)
-- and refuses to commit if PRAGMA foreign_key_check reports any violation
-- afterwards. That is step 1 of SQLite's documented "12-step ALTER TABLE"
-- procedure; PRAGMA defer_foreign_keys is not equivalent, because the counter
-- bumped by DROP TABLE's implicit DELETE is not cleared when the replacement
-- table reuses the same ids. Every other migration keeps foreign keys on.

-- ---------------------------------------------------------------------------
-- storage_policies: enabled + default flags, exactly one default.
-- ---------------------------------------------------------------------------
ALTER TABLE storage_policies ADD COLUMN enabled INTEGER NOT NULL DEFAULT 1
    CHECK (enabled IN (0,1));
ALTER TABLE storage_policies ADD COLUMN is_default INTEGER NOT NULL DEFAULT 0
    CHECK (is_default IN (0,1));

UPDATE storage_policies SET is_default = 1
 WHERE id = (SELECT MIN(id) FROM storage_policies);

CREATE UNIQUE INDEX storage_policies_one_default
    ON storage_policies(is_default) WHERE is_default = 1;

-- ---------------------------------------------------------------------------
-- blobs: rebuild keyed by (storage_policy_id, sha256).
--
-- ids are preserved so nodes.blob_id (and the FTS triggers on nodes) keep
-- working; every existing row belongs to the default policy.
-- ---------------------------------------------------------------------------
CREATE TABLE blobs_new (
    id                INTEGER PRIMARY KEY AUTOINCREMENT,
    storage_policy_id INTEGER NOT NULL REFERENCES storage_policies(id),
    sha256            TEXT    NOT NULL,
    size              INTEGER NOT NULL CHECK(size >= 0),
    refcount          INTEGER NOT NULL DEFAULT 0 CHECK(refcount >= 0),
    created_at        INTEGER NOT NULL,
    UNIQUE(storage_policy_id, sha256)
);

INSERT INTO blobs_new (id, storage_policy_id, sha256, size, refcount, created_at)
SELECT id, (SELECT id FROM storage_policies WHERE is_default = 1), sha256, size, refcount, created_at
  FROM blobs ORDER BY id;

DROP TABLE blobs;
ALTER TABLE blobs_new RENAME TO blobs;

CREATE INDEX blobs_sha256 ON blobs(sha256);
CREATE INDEX blobs_policy_sha256 ON blobs(storage_policy_id, sha256);

-- ---------------------------------------------------------------------------
-- uploads: the policy an upload was started for (nullable so the column can be
-- added; the upload service always writes it). Existing rows belong to the
-- default policy, matching their nodes.
-- ---------------------------------------------------------------------------
ALTER TABLE uploads ADD COLUMN storage_policy_id INTEGER REFERENCES storage_policies(id);
UPDATE uploads SET storage_policy_id = (SELECT id FROM storage_policies WHERE is_default = 1)
 WHERE storage_policy_id IS NULL;

-- ---------------------------------------------------------------------------
-- transfers: same, backfilled from the parent directory the job targets. The
-- nodes backfill below runs later in this file, so the parent policy may still
-- be NULL here; the default policy is the fallback (the transfer service falls
-- back again at download time for any row that stays NULL).
-- ---------------------------------------------------------------------------
ALTER TABLE transfers ADD COLUMN storage_policy_id INTEGER REFERENCES storage_policies(id);
UPDATE transfers
   SET storage_policy_id = COALESCE(
       (SELECT n.storage_policy_id FROM nodes n WHERE n.id = transfers.parent_id),
       (SELECT id FROM storage_policies WHERE is_default = 1))
 WHERE storage_policy_id IS NULL;

-- ---------------------------------------------------------------------------
-- nodes: every node must carry an existing policy.
--
-- The plan calls for storage_policy_id NOT NULL. Rebuilding nodes would force a
-- drop of the nodes table, and with it every index and the FTS triggers added in
-- 0005/0006/0011 — a risky, purely mechanical rewrite. Two triggers enforce the
-- same invariant with no table rebuild: a NULL policy and a dangling policy id
-- both abort the write. This is equivalent to NOT NULL plus the foreign key the
-- column already declares, and it also covers rows written by old code.
-- ---------------------------------------------------------------------------
UPDATE nodes SET storage_policy_id = (SELECT id FROM storage_policies WHERE is_default = 1)
 WHERE storage_policy_id IS NULL;

CREATE TRIGGER nodes_policy_required_insert AFTER INSERT ON nodes
WHEN NEW.storage_policy_id IS NULL
  OR NOT EXISTS (SELECT 1 FROM storage_policies WHERE id = NEW.storage_policy_id)
BEGIN
    SELECT RAISE(ABORT, 'storage policy required');
END;

CREATE TRIGGER nodes_policy_required_update AFTER UPDATE OF storage_policy_id ON nodes
WHEN NEW.storage_policy_id IS NULL
  OR NOT EXISTS (SELECT 1 FROM storage_policies WHERE id = NEW.storage_policy_id)
BEGIN
    SELECT RAISE(ABORT, 'storage policy required');
END;

