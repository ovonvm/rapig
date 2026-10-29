package sqlstore

import (
	"context"
	"fmt"
	"time"

	"rapig/internal/domain/identity"
	"rapig/internal/platform/apperr"
)

// CreateSession persists a login session.
func (d *DB) CreateSession(ctx context.Context, s identity.Session) error {
	_, err := d.ExecContext(ctx,
		`INSERT INTO sessions (id, user_id, csrf_hash, ip, user_agent, created_at, expires_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		s.ID, s.UserID, s.CSRFHash, s.IP, s.UserAgent,
		s.CreatedAt.Unix(), s.ExpiresAt.Unix())
	if err != nil {
		return fmt.Errorf("sqlstore: insert session: %w", err)
	}
	return nil
}

// SessionByID loads a session together with its user. A missing session is
// apperr.ErrNotFound so callers never see raw sql.ErrNoRows.
func (d *DB) SessionByID(ctx context.Context, id string) (identity.Session, identity.User, error) {
	var (
		s       identity.Session
		created int64
		expires int64
	)
	const q = `SELECT id, user_id, csrf_hash, ip, user_agent, created_at, expires_at
		FROM sessions WHERE id = ?`
	err := d.QueryRowContext(ctx, q, id).Scan(
		&s.ID, &s.UserID, &s.CSRFHash, &s.IP, &s.UserAgent, &created, &expires)
	if err != nil {
		if isNoRows(err) {
			return identity.Session{}, identity.User{}, apperr.ErrNotFound
		}
		return identity.Session{}, identity.User{}, fmt.Errorf("sqlstore: session by id: %w", err)
	}
	s.CreatedAt = time.Unix(created, 0).UTC()
	s.ExpiresAt = time.Unix(expires, 0).UTC()

	user, err := d.UserByID(ctx, s.UserID)
	if err != nil {
		return identity.Session{}, identity.User{}, err
	}
	return s, user, nil
}

// DeleteSession revokes one session by id.
func (d *DB) DeleteSession(ctx context.Context, id string) error {
	_, err := d.ExecContext(ctx, `DELETE FROM sessions WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("sqlstore: delete session: %w", err)
	}
	return nil
}

// DeleteUserSessionsExcept revokes every session of a user except keepID (used
// after a password change).
func (d *DB) DeleteUserSessionsExcept(ctx context.Context, userID int64, keepID string) (int64, error) {
	res, err := d.ExecContext(ctx,
		`DELETE FROM sessions WHERE user_id = ? AND id <> ?`, userID, keepID)
	if err != nil {
		return 0, fmt.Errorf("sqlstore: delete user sessions: %w", err)
	}
	return res.RowsAffected()
}

// DeleteExpiredSessions removes sessions that are past expiry; it runs at
// startup and on a timer in serve.
func (d *DB) DeleteExpiredSessions(ctx context.Context, now time.Time) (int64, error) {
	res, err := d.ExecContext(ctx, `DELETE FROM sessions WHERE expires_at <= ?`, now.Unix())
	if err != nil {
		return 0, fmt.Errorf("sqlstore: delete expired sessions: %w", err)
	}
	return res.RowsAffected()
}
