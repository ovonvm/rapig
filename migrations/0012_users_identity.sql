-- M5.2b: self registration and three identifier identity (docs/15-m5-plan.md §11).
--
-- An account always has a username; e-mail and phone are optional second/third
-- login identifiers. The `*_folded` columns hold the lookup/uniqueness form
-- (trimmed, lower cased for e-mail, trimmed for phone) and stay NULL when the
-- identifier is not set, so the partial unique indexes below allow any number
-- of accounts without an e-mail or phone.

ALTER TABLE users ADD COLUMN email TEXT;
ALTER TABLE users ADD COLUMN email_folded TEXT;
ALTER TABLE users ADD COLUMN phone TEXT;
ALTER TABLE users ADD COLUMN phone_folded TEXT;
ALTER TABLE users ADD COLUMN is_root INTEGER NOT NULL DEFAULT 0 CHECK (is_root IN (0, 1));

-- The oldest administrator of an existing installation becomes the root
-- account: the only one allowed to toggle self registration. On a fresh
-- database there is no admin yet, so this is a no-op and identity.Bootstrap
-- creates the root row directly.
UPDATE users SET is_root = 1
WHERE id = (SELECT u.id FROM users u JOIN roles r ON r.id = u.role_id
            WHERE r.code = 'admin' ORDER BY u.id LIMIT 1);

CREATE UNIQUE INDEX users_email_folded ON users (email_folded) WHERE email_folded IS NOT NULL;
CREATE UNIQUE INDEX users_phone_folded ON users (phone_folded) WHERE phone_folded IS NOT NULL;

-- Registered accounts start with 1 GiB (0 means unlimited and is only ever an
-- explicit administrator choice). The old untouched default was '0', so the
-- migration upgrades it; installations without the key get the same value.
UPDATE settings SET value = '1073741824', updated_at = unixepoch()
WHERE key = 'default_quota_bytes' AND value = '0';

INSERT OR IGNORE INTO settings (key, value, updated_at)
VALUES ('default_quota_bytes', '1073741824', unixepoch());
