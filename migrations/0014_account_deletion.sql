-- M5.2c: self-service account deletion with a grace window.
--
-- Deleting an account is a two phase operation:
--
--   * the request marks the account deleted (status 'disabled') and stamps a
--     purge deadline; nothing is destroyed so the owner can cancel with the
--     one-time token issued at request time;
--   * once the deadline passes (or an admin purges immediately) the row is
--     deleted for real and the username becomes a tombstone so a fresh account
--     cannot impersonate the old one for 90 days.
--
-- Purely additive: users keeps its rows, columns are nullable so existing
-- accounts stay untouched. Sessions/shares/transfers are revoked by the
-- service layer, not by triggers.

ALTER TABLE users ADD COLUMN deleted_at INTEGER;
ALTER TABLE users ADD COLUMN purge_after INTEGER;
CREATE INDEX users_purge_after ON users(purge_after) WHERE purge_after IS NOT NULL;

CREATE TABLE username_tombstones (
    username_folded TEXT PRIMARY KEY,
    username        TEXT NOT NULL,
    expires_at      INTEGER NOT NULL,
    created_at      INTEGER NOT NULL
);
CREATE INDEX username_tombstones_expires ON username_tombstones(expires_at);

CREATE TABLE deletion_tokens (
    token_hash TEXT PRIMARY KEY,
    user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    created_at INTEGER NOT NULL,
    expires_at INTEGER NOT NULL,
    used_at    INTEGER
);
CREATE INDEX deletion_tokens_user ON deletion_tokens(user_id);

INSERT OR IGNORE INTO settings(key,value,updated_at) VALUES ('deletion_enabled','0',unixepoch());
