package sqlstore

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// Application-owned setting keys (documented in docs/08-api.md).
const (
	SettingAuditRetentionDays = "audit_retention_days"
	SettingTrashRetentionDays = "trash_retention_days"
	SettingSiteName           = "site_name"
	SettingRegistrationOn     = "registration_enabled"
	SettingDeletionEnabled    = "deletion_enabled"
	SettingDefaultQuotaBytes  = "default_quota_bytes"
)

// Defaults used when a setting is absent or invalid.
const (
	DefaultAuditRetentionDays = 90
	DefaultTrashRetentionDays = 7
	DefaultSiteName           = "Rapig"
)

// Setting reads one key from the settings table. The second result reports
// whether the key exists; callers supply defaults themselves so a missing key
// never needs a write on a read path.
func (d *DB) Setting(ctx context.Context, key string) (string, bool, error) {
	var value string
	err := d.QueryRowContext(ctx,
		`SELECT value FROM settings WHERE key = ?`, key).Scan(&value)
	if err != nil {
		if err == sql.ErrNoRows {
			return "", false, nil
		}
		return "", false, fmt.Errorf("sqlstore: select setting %q: %w", key, err)
	}
	return value, true, nil
}

// SetSetting upserts one key.
func (d *DB) SetSetting(ctx context.Context, key, value string) error {
	now := time.Now().Unix()
	_, err := d.ExecContext(ctx,
		`INSERT INTO settings (key, value, updated_at) VALUES (?, ?, ?)
		 ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`,
		key, value, now)
	if err != nil {
		return fmt.Errorf("sqlstore: upsert setting %q: %w", key, err)
	}
	return nil
}

// AllSettings returns every key for diagnostics and future admin pages.
func (d *DB) AllSettings(ctx context.Context) (map[string]string, error) {
	rows, err := d.QueryContext(ctx, `SELECT key, value FROM settings ORDER BY key`)
	if err != nil {
		return nil, fmt.Errorf("sqlstore: list settings: %w", err)
	}
	defer rows.Close()
	out := make(map[string]string)
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, fmt.Errorf("sqlstore: scan setting: %w", err)
		}
		out[k] = v
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("sqlstore: setting rows: %w", err)
	}
	return out, nil
}
