package sqlstore

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"rapig/internal/domain/access"
	"rapig/internal/domain/share"
	"rapig/internal/platform/apperr"
)

const shareColumns = `s.id, s.token, s.owner_id, s.node_id, s.kind, s.password_hash,
	s.expires_at, s.max_downloads, s.download_count, s.visits_count,
	s.last_access_at, s.created_at, s.revoked_at`

type shareScanner interface {
	Scan(dest ...any) error
}

func scanShare(row shareScanner) (share.Share, error) {
	var (
		out                   share.Share
		kind                  string
		expiresAt, lastAccess sql.NullInt64
		createdAt             int64
		revokedAt             sql.NullInt64
	)
	if err := row.Scan(&out.ID, &out.Token, &out.OwnerID, &out.NodeID, &kind,
		&out.PasswordHash, &expiresAt, &out.MaxDownloads, &out.DownloadCount,
		&out.VisitsCount, &lastAccess, &createdAt, &revokedAt); err != nil {
		return share.Share{}, err
	}
	out.Kind = share.Kind(kind)
	out.CreatedAt = unixUTC(createdAt)
	if expiresAt.Valid {
		t := unixUTC(expiresAt.Int64)
		out.ExpiresAt = &t
	}
	if lastAccess.Valid {
		t := unixUTC(lastAccess.Int64)
		out.LastAccessAt = &t
	}
	if revokedAt.Valid {
		t := unixUTC(revokedAt.Int64)
		out.RevokedAt = &t
	}
	return out, nil
}

// shareItems loads the member node ids of one share, ascending.
func (d *DB) shareItems(ctx context.Context, shareID string) ([]int64, error) {
	rows, err := d.QueryContext(ctx, `SELECT node_id FROM share_items
		WHERE share_id = ? ORDER BY node_id`, shareID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]int64, 0, 4)
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

func (d *DB) loadShareItems(ctx context.Context, sh *share.Share) error {
	items, err := d.shareItems(ctx, sh.ID)
	if err != nil {
		return err
	}
	sh.Items = items
	return nil
}

// CreateShare inserts one share plus its member set. Every member must be an
// active, non-root node visible to the scope user; the INSERT ... SELECT keeps
// that check in SQL so a service bug cannot create a cross-owner, cross-group or
// root share.
func (d *DB) CreateShare(ctx context.Context, sc access.Scope, sh share.Share, items []int64) (share.Share, error) {
	if err := sc.Validate(); err != nil {
		return share.Share{}, err
	}
	if len(items) == 0 {
		return share.Share{}, apperr.ErrBadRequest.WithReason(apperr.ReasonInvalidID)
	}
	var expires any
	if sh.ExpiresAt != nil {
		expires = sh.ExpiresAt.Unix()
	}
	accessSQL, accessArgs := nodeAccessSQL(sc)
	insertShare := `INSERT INTO shares (id, token, owner_id, node_id, kind, password_hash,
		expires_at, max_downloads, download_count, visits_count, created_at)
		SELECT ?, ?, ?, id, ?, ?, ?, ?, 0, 0, ?
		FROM nodes WHERE id = ? AND ` + accessSQL + ` AND state = 'active' AND parent_id IS NOT NULL`
	insertItem := `INSERT INTO share_items (share_id, node_id)
		SELECT ?, id FROM nodes WHERE id = ? AND ` + accessSQL + ` AND state = 'active' AND parent_id IS NOT NULL`
	shareArgs := append([]any{sh.ID, sh.Token, sc.UserID, string(sh.Kind), sh.PasswordHash, expires,
		sh.MaxDownloads, sh.CreatedAt.Unix(), sh.NodeID}, accessArgs...)
	err := d.WithTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, insertShare, shareArgs...)
		if err != nil {
			if isForeignKeyViolation(err) {
				return apperr.ErrNotFound
			}
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return apperr.ErrNotFound
		}
		for _, id := range items {
			itemArgs := append([]any{sh.ID, id}, accessArgs...)
			res, err := tx.ExecContext(ctx, insertItem, itemArgs...)
			if err != nil {
				if isForeignKeyViolation(err) {
					return apperr.ErrNotFound
				}
				return err
			}
			if n, _ := res.RowsAffected(); n == 0 {
				return apperr.ErrNotFound
			}
		}
		return nil
	})
	if err != nil {
		return share.Share{}, err
	}
	return d.ShareByID(ctx, sc, sh.ID)
}

// ShareByID loads one share owned by the scope user, including its items.
func (d *DB) ShareByID(ctx context.Context, sc access.Scope, id string) (share.Share, error) {
	if err := sc.Validate(); err != nil {
		return share.Share{}, err
	}
	row := d.QueryRowContext(ctx, `SELECT `+shareColumns+` FROM shares s
		WHERE s.id = ? AND s.owner_id = ?`, id, sc.UserID)
	out, err := scanShare(row)
	if isNoRows(err) {
		return share.Share{}, apperr.ErrNotFound
	}
	if err != nil {
		return share.Share{}, err
	}
	if err := d.loadShareItems(ctx, &out); err != nil {
		return share.Share{}, err
	}
	return out, nil
}

// ListShares pages through the caller's shares, newest first. status is one of
// "" / "all", "active", "revoked", "expired".
func (d *DB) ListShares(ctx context.Context, sc access.Scope, status string, limit, offset int, now time.Time) ([]share.Share, int, error) {
	if err := sc.Validate(); err != nil {
		return nil, 0, err
	}
	where := `s.owner_id = ?`
	args := []any{sc.UserID}
	switch status {
	case "", "all":
	case "active":
		where += ` AND s.revoked_at IS NULL AND (s.expires_at IS NULL OR s.expires_at > ?)`
		args = append(args, now.Unix())
	case "revoked":
		where += ` AND s.revoked_at IS NOT NULL`
	case "expired":
		where += ` AND s.revoked_at IS NULL AND s.expires_at IS NOT NULL AND s.expires_at <= ?`
		args = append(args, now.Unix())
	default:
		return nil, 0, apperr.ErrBadRequest.WithReason(apperr.ReasonInvalidStatus)
	}
	var total int
	countArgs := append([]any{}, args...)
	if err := d.QueryRowContext(ctx, `SELECT COUNT(*) FROM shares s WHERE `+where, countArgs...).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := d.QueryContext(ctx, `SELECT `+shareColumns+` FROM shares s
		WHERE `+where+` ORDER BY s.created_at DESC, s.id DESC LIMIT ? OFFSET ?`,
		append(args, limit, offset)...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := make([]share.Share, 0, limit)
	for rows.Next() {
		sh, err := scanShare(rows)
		if err != nil {
			return nil, 0, err
		}
		if err := d.loadShareItems(ctx, &sh); err != nil {
			return nil, 0, err
		}
		out = append(out, sh)
	}
	return out, total, rows.Err()
}

// RevokeShare marks a share revoked. Returns false when it was already revoked.
func (d *DB) RevokeShare(ctx context.Context, sc access.Scope, id string, now time.Time) (bool, error) {
	if err := sc.Validate(); err != nil {
		return false, err
	}
	res, err := d.ExecContext(ctx, `UPDATE shares SET revoked_at = ?
		WHERE id = ? AND owner_id = ? AND revoked_at IS NULL`, now.Unix(), id, sc.UserID)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// RestoreShare clears revoked_at. It refuses revoked-but-expired shares: the
// token must never silently outlive the expiry the owner chose.
func (d *DB) RestoreShare(ctx context.Context, sc access.Scope, id string, now time.Time) (bool, error) {
	if err := sc.Validate(); err != nil {
		return false, err
	}
	res, err := d.ExecContext(ctx, `UPDATE shares SET revoked_at = NULL
		WHERE id = ? AND owner_id = ? AND revoked_at IS NOT NULL
		  AND (expires_at IS NULL OR expires_at > ?)`, id, sc.UserID, now.Unix())
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// PurgeShare physically removes a dead share (revoked or expired). Active
// shares cannot be purged; callers must revoke first.
func (d *DB) PurgeShare(ctx context.Context, sc access.Scope, id string, now time.Time) (bool, error) {
	if err := sc.Validate(); err != nil {
		return false, err
	}
	res, err := d.ExecContext(ctx, `DELETE FROM shares
		WHERE id = ? AND owner_id = ?
		  AND (revoked_at IS NOT NULL OR (expires_at IS NOT NULL AND expires_at <= ?))`,
		id, sc.UserID, now.Unix())
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// ShareByToken is the public lookup; it has no scope because the token itself is
// the capability. Callers must validate node state and expiry.
func (d *DB) ShareByToken(ctx context.Context, token string) (share.Share, error) {
	row := d.QueryRowContext(ctx, `SELECT `+shareColumns+` FROM shares s WHERE s.token = ?`, token)
	out, err := scanShare(row)
	if isNoRows(err) {
		return share.Share{}, apperr.ErrNotFound
	}
	if err != nil {
		return share.Share{}, err
	}
	if err := d.loadShareItems(ctx, &out); err != nil {
		return share.Share{}, err
	}
	return out, nil
}

// TouchVisit records a successful public metadata/list access.
func (d *DB) TouchVisit(ctx context.Context, id string, now time.Time) error {
	_, err := d.ExecContext(ctx, `UPDATE shares
		SET visits_count = visits_count + 1, last_access_at = ? WHERE id = ?`, now.Unix(), id)
	return err
}

// TryConsumeDownload atomically increments download_count if the share is still
// usable and under its download budget. Returns false when the budget is
// exhausted (or the share expired/revoked in the meantime).
func (d *DB) TryConsumeDownload(ctx context.Context, id string, now time.Time) (bool, error) {
	res, err := d.ExecContext(ctx, `UPDATE shares
		SET download_count = download_count + 1, last_access_at = ?
		WHERE id = ? AND revoked_at IS NULL
		  AND (expires_at IS NULL OR expires_at > ?)
		  AND (max_downloads = 0 OR download_count < max_downloads)`,
		now.Unix(), id, now.Unix())
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// ShareScope resolves the repository scope for anonymous share access. A share
// token is a capability: it stands in for the owner's identity, so the scope
// keeps the owner id and adds the node's group when the shared node lives in a
// group space. Callers still enforce the subtree boundary in the service layer.
func (d *DB) ShareScope(ctx context.Context, nodeID int64) (access.Scope, error) {
	var (
		ownerID int64
		groupID sql.NullInt64
	)
	err := d.QueryRowContext(ctx, `SELECT owner_id, group_id FROM nodes WHERE id = ?`, nodeID).
		Scan(&ownerID, &groupID)
	if isNoRows(err) {
		return access.Scope{}, apperr.ErrNotFound
	}
	if err != nil {
		return access.Scope{}, fmt.Errorf("sqlstore: share scope: %w", err)
	}
	if !groupID.Valid {
		return access.UserScope(ownerID, false), nil
	}
	return access.UserGroupScope(ownerID, false, []int64{groupID.Int64}), nil
}

// DeleteDeadShares removes revoked/expired rows older than the stats retention
// cutoff. Used by the maintenance loop.
func (d *DB) DeleteDeadShares(ctx context.Context, cutoff time.Time) (int64, error) {
	res, err := d.ExecContext(ctx, `DELETE FROM shares
		WHERE (revoked_at IS NOT NULL AND revoked_at < ?)
		   OR (expires_at IS NOT NULL AND expires_at < ?)`, cutoff.Unix(), cutoff.Unix())
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, nil
}
