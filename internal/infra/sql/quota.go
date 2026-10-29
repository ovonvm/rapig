package sqlstore

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"rapig/internal/platform/apperr"
)

// ReserveBytes books n bytes of an upload against the user's quota. The
// comparison happens inside the UPDATE so concurrent uploads cannot overbook;
// a user with quota_bytes = 0 is unlimited.
func (d *DB) ReserveBytes(ctx context.Context, userID, n int64) error {
	if n < 0 {
		return apperr.ErrBadRequest
	}
	res, err := d.ExecContext(ctx,
		`UPDATE users SET reserved_bytes = reserved_bytes + ?, updated_at = ?
		 WHERE id = ?
		   AND (quota_bytes = 0 OR used_bytes + reserved_bytes + ? <= quota_bytes)`,
		n, time.Now().Unix(), userID, n)
	if err != nil {
		return fmt.Errorf("sqlstore: reserve bytes: %w", err)
	}
	if n, _ := res.RowsAffected(); n > 0 {
		return nil
	}
	// No row updated: either the user vanished or the quota is full.
	var quota int64
	err = d.QueryRowContext(ctx, `SELECT quota_bytes FROM users WHERE id = ?`, userID).Scan(&quota)
	switch {
	case err == sql.ErrNoRows:
		return apperr.ErrNotFound
	case err != nil:
		return fmt.Errorf("sqlstore: check quota: %w", err)
	default:
		return apperr.ErrQuotaExceeded.WithReason(apperr.ReasonQuotaExceeded)
	}
}

// ReleaseBytes gives back reserved bytes after a cancel or expiry. Clamped at
// zero so a double release can never corrupt the counter.
func (d *DB) ReleaseBytes(ctx context.Context, userID, n int64) error {
	if n <= 0 {
		return nil
	}
	_, err := d.ExecContext(ctx,
		`UPDATE users SET reserved_bytes = CASE WHEN reserved_bytes >= ? THEN reserved_bytes - ? ELSE 0 END,
		   updated_at = ?
		 WHERE id = ?`, n, n, time.Now().Unix(), userID)
	if err != nil {
		return fmt.Errorf("sqlstore: release bytes: %w", err)
	}
	return nil
}

// ConsumeBytes charges the used quota without a reservation. It is used by
// the instant-upload (dedup) path where no upload session is created.
func (d *DB) ConsumeBytes(ctx context.Context, userID, n int64) error {
	res, err := d.ExecContext(ctx,
		`UPDATE users SET used_bytes = used_bytes + ?, updated_at = ?
		 WHERE id = ?
		   AND (quota_bytes = 0 OR used_bytes + reserved_bytes + ? <= quota_bytes)`,
		n, time.Now().Unix(), userID, n)
	if err != nil {
		return fmt.Errorf("sqlstore: consume bytes: %w", err)
	}
	if n, _ := res.RowsAffected(); n > 0 {
		return nil
	}
	var quota int64
	err = d.QueryRowContext(ctx, `SELECT quota_bytes FROM users WHERE id = ?`, userID).Scan(&quota)
	switch {
	case err == sql.ErrNoRows:
		return apperr.ErrNotFound
	case err != nil:
		return fmt.Errorf("sqlstore: check quota: %w", err)
	default:
		return apperr.ErrQuotaExceeded.WithReason(apperr.ReasonQuotaExceeded)
	}
}

// UserQuota reports used and reserved bytes for a user.
func (d *DB) UserQuota(ctx context.Context, userID int64) (used, reserved int64, err error) {
	err = d.QueryRowContext(ctx,
		`SELECT used_bytes, reserved_bytes FROM users WHERE id = ?`, userID).
		Scan(&used, &reserved)
	if err == sql.ErrNoRows {
		return 0, 0, apperr.ErrNotFound
	}
	if err != nil {
		return 0, 0, fmt.Errorf("sqlstore: user quota: %w", err)
	}
	return used, reserved, nil
}
