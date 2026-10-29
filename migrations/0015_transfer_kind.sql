-- 0015_transfer_kind: qBittorrent offline downloads (M6.1).
--
-- kind selects the download backend; existing rows are HTTP jobs. qb_hash is
-- the lowercase hex BitTorrent v1 infohash and qb_category the qBittorrent
-- category used to scope tasks to one Rapig owner.
ALTER TABLE transfers ADD COLUMN kind TEXT NOT NULL DEFAULT 'http'
	CHECK (kind IN ('http', 'qb'));
ALTER TABLE transfers ADD COLUMN qb_hash TEXT NOT NULL DEFAULT '';
ALTER TABLE transfers ADD COLUMN qb_category TEXT NOT NULL DEFAULT '';

-- Worker lookups only ever query qb rows that carry a hash.
CREATE INDEX idx_transfers_qb_hash ON transfers (qb_hash) WHERE qb_hash <> '';
