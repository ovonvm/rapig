-- 0003: M1 identity hardening and resumable uploads.
--
-- Only additive: 0001/0002 must never be edited (the migrator checksums
-- applied files). Local M0/M0.5 data directories may be deleted, no upgrade
-- of production data is promised before M1 (see docs/09-m1-plan.md §4).

ALTER TABLE users ADD COLUMN failed_login_count INTEGER NOT NULL DEFAULT 0;
ALTER TABLE users ADD COLUMN locked_until INTEGER;
ALTER TABLE users ADD COLUMN reserved_bytes INTEGER NOT NULL DEFAULT 0 CHECK (reserved_bytes >= 0);
ALTER TABLE users ADD COLUMN password_changed_at INTEGER;

ALTER TABLE nodes ADD COLUMN mime TEXT NOT NULL DEFAULT '';

CREATE TABLE uploads (
  id TEXT PRIMARY KEY,
  owner_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  parent_id INTEGER NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
  name TEXT NOT NULL,
  name_folded TEXT NOT NULL,
  size INTEGER NOT NULL CHECK (size >= 0),
  sha256 TEXT NOT NULL DEFAULT '',
  part_size INTEGER NOT NULL,
  parts_total INTEGER NOT NULL,
  received_bytes INTEGER NOT NULL DEFAULT 0,
  state TEXT NOT NULL DEFAULT 'open' CHECK (state IN ('open','completed','canceled')),
  node_id INTEGER REFERENCES nodes(id),
  created_at INTEGER NOT NULL,
  expires_at INTEGER NOT NULL
);
CREATE INDEX uploads_expires ON uploads(expires_at, state);
CREATE INDEX uploads_owner ON uploads(owner_id, state);

CREATE TABLE upload_parts (
  upload_id TEXT NOT NULL REFERENCES uploads(id) ON DELETE CASCADE,
  part_no INTEGER NOT NULL,
  offset INTEGER NOT NULL,
  size INTEGER NOT NULL CHECK (size >= 0),
  sha256 TEXT NOT NULL DEFAULT '',
  PRIMARY KEY (upload_id, part_no)
);

CREATE TABLE breakglass_grants (
  id TEXT PRIMARY KEY,
  actor_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  target_kind TEXT NOT NULL,
  target_id INTEGER NOT NULL,
  reason TEXT NOT NULL,
  created_at INTEGER NOT NULL,
  expires_at INTEGER NOT NULL,
  ended_at INTEGER
);
CREATE INDEX breakglass_active ON breakglass_grants(actor_id, expires_at) WHERE ended_at IS NULL;
