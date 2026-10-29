package sqlstore

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"rapig/internal/domain/identity"
	"rapig/internal/platform/apperr"
)

// nullTime maps an unset timestamp column to NULL.
func nullTime(t *time.Time) any {
	if t == nil {
		return nil
	}
	return t.Unix()
}

// execer is the shared subset of *DB and *sql.Tx so owner-wide revocations can
// run both standalone and inside the deletion transaction.
type execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// RevokeActiveSharesByOwner revokes every share an account still publishes
// (not revoked and not expired). Deletion makes all links dead immediately,
// even during the grace window.
func (d *DB) RevokeActiveSharesByOwner(ctx context.Context, ownerID int64, now time.Time) (int64, error) {
	return revokeSharesByOwner(ctx, d, ownerID, now)
}

func revokeSharesByOwner(ctx context.Context, ex execer, ownerID int64, now time.Time) (int64, error) {
	res, err := ex.ExecContext(ctx, `UPDATE shares SET revoked_at = ?
		WHERE owner_id = ? AND revoked_at IS NULL
		  AND (expires_at IS NULL OR expires_at > ?)`,
		now.Unix(), ownerID, now.Unix())
	if err != nil {
		return 0, fmt.Errorf("sqlstore: revoke shares by owner: %w", err)
	}
	return res.RowsAffected()
}

// CancelActiveTransfersByOwner cancels pending/running/paused jobs of an
// account; those jobs will never complete because the owner can no longer log
// in and their nodes are about to disappear.
func (d *DB) CancelActiveTransfersByOwner(ctx context.Context, ownerID int64, now time.Time) (int64, error) {
	return cancelTransfersByOwner(ctx, d, ownerID, now)
}

func cancelTransfersByOwner(ctx context.Context, ex execer, ownerID int64, now time.Time) (int64, error) {
	res, err := ex.ExecContext(ctx, `UPDATE transfers
		SET state = 'canceled', finished_at = ?, updated_at = ?
		WHERE owner_id = ? AND state IN ('pending','running','paused')`,
		now.Unix(), now.Unix(), ownerID)
	if err != nil {
		return 0, fmt.Errorf("sqlstore: cancel transfers by owner: %w", err)
	}
	return res.RowsAffected()
}

// ---- deletion state --------------------------------------------------------

// SetUserDeletion stamps (or with nil times clears) the deletion window on an
// account and writes its status. Callers pass time.Now derived values so the
// service layer decides the purge deadline.
func (d *DB) SetUserDeletion(ctx context.Context, userID int64, status string, deletedAt, purgeAfter *time.Time) error {
	res, err := d.ExecContext(ctx,
		`UPDATE users SET status = ?, deleted_at = ?, purge_after = ?, updated_at = ?
		 WHERE id = ?`,
		status, nullTime(deletedAt), nullTime(purgeAfter), time.Now().Unix(), userID)
	if err != nil {
		return fmt.Errorf("sqlstore: set user deletion: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return apperr.ErrNotFound
	}
	return nil
}

// UsersDueForPurge returns accounts whose grace window has expired, oldest
// deadline first, capped at limit.
func (d *DB) UsersDueForPurge(ctx context.Context, now time.Time, limit int) ([]identity.User, error) {
	rows, err := d.QueryContext(ctx, `SELECT `+userColumns+`
		FROM users u JOIN roles r ON r.id = u.role_id
		WHERE u.purge_after IS NOT NULL AND u.purge_after <= ?
		ORDER BY u.purge_after, u.id LIMIT ?`, now.Unix(), limit)
	if err != nil {
		return nil, fmt.Errorf("sqlstore: users due for purge: %w", err)
	}
	defer rows.Close()
	out := make([]identity.User, 0, limit)
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// CountActiveAdminsExcluding counts enabled administrators other than
// excludeID. It backs the "cannot delete the last admin" guard.
func (d *DB) CountActiveAdminsExcluding(ctx context.Context, excludeID int64) (int, error) {
	var n int
	err := d.QueryRowContext(ctx, `SELECT COUNT(*) FROM users u
		JOIN roles r ON r.id = u.role_id
		WHERE r.code = 'admin' AND u.status = 'active' AND u.id <> ?`, excludeID).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("sqlstore: count active admins: %w", err)
	}
	return n, nil
}

// BeginUserDeletion performs the whole self-service deletion request in one
// transaction: the account is disabled and stamped with the grace window, every
// session and active share is revoked, active transfers are canceled and the
// one-time cancel token is stored. Doing it atomically keeps a crash from
// leaving a disabled account without a way to cancel.
func (d *DB) BeginUserDeletion(ctx context.Context, userID int64, now, purgeAfter time.Time, tokenHash string) error {
	return d.WithTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx,
			`UPDATE users SET status = 'disabled', deleted_at = ?, purge_after = ?, updated_at = ?
			 WHERE id = ?`, now.Unix(), purgeAfter.Unix(), now.Unix(), userID)
		if err != nil {
			return fmt.Errorf("sqlstore: begin user deletion: %w", err)
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return apperr.ErrNotFound
		}
		if _, err := revokeSharesByOwner(ctx, tx, userID, now); err != nil {
			return err
		}
		if _, err := cancelTransfersByOwner(ctx, tx, userID, now); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM sessions WHERE user_id = ?`, userID); err != nil {
			return fmt.Errorf("sqlstore: revoke sessions during deletion: %w", err)
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO deletion_tokens (token_hash, user_id, created_at, expires_at, used_at)
			 VALUES (?, ?, ?, ?, NULL)`,
			tokenHash, userID, now.Unix(), purgeAfter.Unix()); err != nil {
			return fmt.Errorf("sqlstore: insert deletion token: %w", err)
		}
		return nil
	})
}

// PurgeUserAccount destroys one account for good: its username becomes a
// tombstone until tombstoneUntil and the row is deleted, cascading nodes,
// uploads, shares, transfers, sessions and deletion tokens. The audit log is
// preserved (no foreign key).
func (d *DB) PurgeUserAccount(ctx context.Context, user identity.User, now, tombstoneUntil time.Time) error {
	return d.WithTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO username_tombstones (username_folded, username, expires_at, created_at)
			 VALUES (?, ?, ?, ?)
			 ON CONFLICT(username_folded) DO UPDATE SET
			   username = excluded.username,
			   expires_at = excluded.expires_at,
			   created_at = excluded.created_at`,
			user.UsernameFolded, user.Username, tombstoneUntil.Unix(), now.Unix()); err != nil {
			return fmt.Errorf("sqlstore: insert tombstone during purge: %w", err)
		}
		// P1: group content outlives its uploader. Hand every node the user owns
		// inside a group to a successor before the row disappears; only personal
		// nodes are destroyed with the account.
		if err := transferGroupNodes(ctx, tx, user.ID); err != nil {
			return err
		}
		// F-46: the user's nodes disappear through ON DELETE CASCADE, but
		// blob rows are shared (keyed by policy+sha256) and their refcount is
		// derived from the nodes still pointing at them. Release this user's
		// references first, otherwise the object would look referenced forever
		// and only `rapig fsck -repair` could bring the count back in line.
		// Group nodes were re-owned above, so they no longer count here.
		if _, err := tx.ExecContext(ctx, `
			UPDATE blobs
			SET refcount = MAX(0, refcount - (
				SELECT COUNT(*) FROM nodes n
				WHERE n.blob_id = blobs.id AND n.owner_id = ?
			))
			WHERE id IN (SELECT blob_id FROM nodes WHERE owner_id = ? AND blob_id IS NOT NULL)`,
			user.ID, user.ID); err != nil {
			return fmt.Errorf("sqlstore: release blob references during purge: %w", err)
		}
		res, err := tx.ExecContext(ctx, `DELETE FROM users WHERE id = ?`, user.ID)
		if err != nil {
			return fmt.Errorf("sqlstore: delete user during purge: %w", err)
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return apperr.ErrNotFound
		}
		return nil
	})
}

// transferGroupNodes re-owns the group nodes of a departing user. The order is
// fixed: the group creator, then the smallest remaining manager, then the
// smallest remaining member, and finally the smallest admin — there is no
// "orphan" state, because a group node must always have a live owner (its
// owner_id is the account the quota was charged to).
func transferGroupNodes(ctx context.Context, tx *sql.Tx, userID int64) error {
	rows, err := tx.QueryContext(ctx,
		`SELECT DISTINCT group_id FROM nodes WHERE owner_id = ? AND group_id IS NOT NULL`, userID)
	if err != nil {
		return fmt.Errorf("sqlstore: list group nodes during purge: %w", err)
	}
	var groupIDs []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return fmt.Errorf("sqlstore: scan group id during purge: %w", err)
		}
		groupIDs = append(groupIDs, id)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("sqlstore: list group nodes during purge: %w", err)
	}
	rows.Close()

	for _, groupID := range groupIDs {
		successor, err := groupSuccessor(ctx, tx, groupID, userID)
		if err != nil {
			return err
		}
		if successor == 0 {
			continue
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE nodes SET owner_id = ? WHERE owner_id = ? AND group_id = ?`,
			successor, userID, groupID); err != nil {
			return fmt.Errorf("sqlstore: transfer group nodes during purge: %w", err)
		}
		// Keep "at least one manager" true when the departing user was the
		// group's only manager: the cascade would otherwise leave the group
		// unmanageable.
		var managers int
		if err := tx.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM group_members WHERE group_id = ? AND role = 'manager' AND user_id != ?`,
			groupID, userID).Scan(&managers); err != nil {
			return fmt.Errorf("sqlstore: count group managers during purge: %w", err)
		}
		if managers > 0 {
			continue
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE group_members SET role = 'manager' WHERE group_id = ? AND user_id = ?`,
			groupID, successor); err != nil {
			return fmt.Errorf("sqlstore: promote group manager during purge: %w", err)
		}
	}
	return nil
}

// groupSuccessor picks the next owner for a departing user's group nodes.
func groupSuccessor(ctx context.Context, tx *sql.Tx, groupID, userID int64) (int64, error) {
	var id sql.NullInt64
	err := tx.QueryRowContext(ctx, `
		SELECT COALESCE(
		  (SELECT g.created_by FROM groups g WHERE g.id = ? AND g.created_by != ?),
		  (SELECT MIN(m.user_id) FROM group_members m
			WHERE m.group_id = ? AND m.role = 'manager' AND m.user_id != ?),
		  (SELECT MIN(m.user_id) FROM group_members m
			WHERE m.group_id = ? AND m.user_id != ?),
		  (SELECT MIN(u.id) FROM users u JOIN roles r ON r.id = u.role_id
			WHERE r.code = 'admin' AND u.id != ?)
		)`,
		groupID, userID, groupID, userID, groupID, userID, userID).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("sqlstore: pick group successor during purge: %w", err)
	}
	return id.Int64, nil
}

// ---- username tombstones ---------------------------------------------------
// UsernameTombstoneExists reports whether folded is reserved at now.
func (d *DB) UsernameTombstoneExists(ctx context.Context, folded string, now time.Time) (bool, error) {
	var n int
	err := d.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM username_tombstones WHERE username_folded = ? AND expires_at > ?`,
		folded, now.Unix()).Scan(&n)
	if err != nil {
		return false, fmt.Errorf("sqlstore: check username tombstone: %w", err)
	}
	return n > 0, nil
}

// PurgeExpiredTombstones drops reservations that no longer block anything.
func (d *DB) PurgeExpiredTombstones(ctx context.Context, now time.Time) (int64, error) {
	res, err := d.ExecContext(ctx,
		`DELETE FROM username_tombstones WHERE expires_at <= ?`, now.Unix())
	if err != nil {
		return 0, fmt.Errorf("sqlstore: purge username tombstones: %w", err)
	}
	return res.RowsAffected()
}

// ---- deletion tokens -------------------------------------------------------

// DeletionToken is a one-time cancel credential. Only the sha256 of the token
// is stored; the raw value is returned to the client exactly once.
type DeletionToken struct {
	TokenHash string
	UserID    int64
	CreatedAt time.Time
	ExpiresAt time.Time
	UsedAt    *time.Time
}

// DeletionTokenByHash loads one token. Missing tokens map to ErrNotFound; the
// service turns that into an opaque bad_request for clients.
func (d *DB) DeletionTokenByHash(ctx context.Context, hash string) (DeletionToken, error) {
	var (
		t       DeletionToken
		created int64
		expires int64
		used    sql.NullInt64
	)
	err := d.QueryRowContext(ctx,
		`SELECT token_hash, user_id, created_at, expires_at, used_at
		 FROM deletion_tokens WHERE token_hash = ?`, hash).
		Scan(&t.TokenHash, &t.UserID, &created, &expires, &used)
	if err != nil {
		if err == sql.ErrNoRows {
			return DeletionToken{}, apperr.ErrNotFound
		}
		return DeletionToken{}, fmt.Errorf("sqlstore: deletion token by hash: %w", err)
	}
	t.CreatedAt = time.Unix(created, 0).UTC()
	t.ExpiresAt = time.Unix(expires, 0).UTC()
	if used.Valid {
		u := time.Unix(used.Int64, 0).UTC()
		t.UsedAt = &u
	}
	return t, nil
}

// MarkDeletionTokenUsed burns one token. It returns the affected user id and
// reports false when the token was already used (or never existed).
func (d *DB) MarkDeletionTokenUsed(ctx context.Context, hash string, now time.Time) (int64, bool, error) {
	var userID int64
	err := d.WithTx(ctx, func(tx *sql.Tx) error {
		err := tx.QueryRowContext(ctx,
			`SELECT user_id FROM deletion_tokens WHERE token_hash = ? AND used_at IS NULL`,
			hash).Scan(&userID)
		if err == sql.ErrNoRows {
			return apperr.ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("sqlstore: select deletion token: %w", err)
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE deletion_tokens SET used_at = ? WHERE token_hash = ? AND used_at IS NULL`,
			now.Unix(), hash); err != nil {
			return fmt.Errorf("sqlstore: mark deletion token used: %w", err)
		}
		return nil
	})
	if err != nil {
		if apperr.CodeOf(err) == apperr.CodeNotFound {
			return 0, false, nil
		}
		return 0, false, err
	}
	return userID, true, nil
}

// MarkUserDeletionTokensUsed burns every outstanding token of a user (an
// admin restore or purge makes them meaningless).
func (d *DB) MarkUserDeletionTokensUsed(ctx context.Context, userID int64, now time.Time) (int64, error) {
	res, err := d.ExecContext(ctx,
		`UPDATE deletion_tokens SET used_at = ? WHERE user_id = ? AND used_at IS NULL`,
		now.Unix(), userID)
	if err != nil {
		return 0, fmt.Errorf("sqlstore: mark user deletion tokens used: %w", err)
	}
	return res.RowsAffected()
}
