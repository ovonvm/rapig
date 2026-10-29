-- M2: optimistic concurrency token for node writes. A monotonic counter beats
-- updated_at because unix second timestamps cannot tell two writes in the same
-- second apart (the first stale-write test proved that).

ALTER TABLE nodes ADD COLUMN rev INTEGER NOT NULL DEFAULT 0;
