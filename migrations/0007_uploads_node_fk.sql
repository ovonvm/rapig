-- M2: uploads.node_id was declared with the default NO ACTION, so purging a
-- node that a completed upload points at failed with a foreign key error
-- (found by Linux acceptance). Rebuild uploads with ON DELETE SET NULL so a
-- completed upload row never blocks node deletion. upload_parts is rebuilt in
-- the same migration because its foreign key names the table being replaced.

CREATE TABLE uploads_new (
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
  node_id INTEGER REFERENCES nodes(id) ON DELETE SET NULL,
  created_at INTEGER NOT NULL,
  expires_at INTEGER NOT NULL
);

INSERT INTO uploads_new (id, owner_id, parent_id, name, name_folded, size, sha256,
  part_size, parts_total, received_bytes, state, node_id, created_at, expires_at)
SELECT id, owner_id, parent_id, name, name_folded, size, sha256,
  part_size, parts_total, received_bytes, state, node_id, created_at, expires_at
FROM uploads;

CREATE TABLE upload_parts_new (
  upload_id TEXT NOT NULL REFERENCES uploads_new(id) ON DELETE CASCADE,
  part_no INTEGER NOT NULL,
  offset INTEGER NOT NULL,
  size INTEGER NOT NULL CHECK (size >= 0),
  sha256 TEXT NOT NULL DEFAULT '',
  PRIMARY KEY (upload_id, part_no)
);

INSERT INTO upload_parts_new (upload_id, part_no, offset, size, sha256)
SELECT upload_id, part_no, offset, size, sha256 FROM upload_parts;

DROP TABLE upload_parts;
DROP TABLE uploads;
ALTER TABLE uploads_new RENAME TO uploads;
ALTER TABLE upload_parts_new RENAME TO upload_parts;

CREATE INDEX uploads_expires ON uploads(expires_at, state);
CREATE INDEX uploads_owner ON uploads(owner_id, state);
