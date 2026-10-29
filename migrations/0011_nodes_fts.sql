-- M5.1: filename search index (FTS5 trigram).
-- The index stores the folded (case-insensitive) name so ordering and latin
-- case folding stay consistent with name_folded; the display name is read from
-- nodes. Trigram matches substrings of >=3 runes; shorter queries fall back to
-- an escaped LIKE scan in the repository.

CREATE VIRTUAL TABLE IF NOT EXISTS nodes_fts USING fts5(name_folded, tokenize='trigram');

INSERT INTO nodes_fts (rowid, name_folded)
SELECT id, name_folded FROM nodes;

CREATE TRIGGER IF NOT EXISTS nodes_fts_ai AFTER INSERT ON nodes BEGIN
  INSERT INTO nodes_fts (rowid, name_folded) VALUES (new.id, new.name_folded);
END;

CREATE TRIGGER IF NOT EXISTS nodes_fts_ad AFTER DELETE ON nodes BEGIN
  DELETE FROM nodes_fts WHERE rowid = old.id;
END;

CREATE TRIGGER IF NOT EXISTS nodes_fts_au AFTER UPDATE OF name_folded ON nodes BEGIN
  UPDATE nodes_fts SET name_folded = new.name_folded WHERE rowid = new.id;
END;
