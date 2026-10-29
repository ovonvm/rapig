package sqlstore

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"rapig/internal/domain/identity"
	"rapig/internal/platform/apperr"
)

const userColumns = `u.id, u.username, u.username_folded, u.display_name, u.password_hash,
	u.role_id, r.code, u.status, u.used_bytes, u.quota_bytes, u.reserved_bytes,
	u.failed_login_count, u.created_at, u.updated_at, u.last_login_at,
	u.locked_until, u.password_changed_at, u.email, u.email_folded, u.phone,
	u.phone_folded, u.is_root, u.can_create_groups, u.deleted_at, u.purge_after`

// UserByUsername looks up a user by the folded username. Missing users map to
// apperr.ErrInvalidCredentials at the service layer (never ErrNotFound) so the
// login flow does not leak account existence.
func (d *DB) UserByUsername(ctx context.Context, folded string) (identity.User, error) {
	q := `SELECT ` + userColumns + `
		FROM users u JOIN roles r ON r.id = u.role_id
		WHERE u.username_folded = ?`
	return scanUser(d.QueryRowContext(ctx, q, folded))
}

// UserByEmail looks up a user by the folded e-mail address.
func (d *DB) UserByEmail(ctx context.Context, folded string) (identity.User, error) {
	q := `SELECT ` + userColumns + `
		FROM users u JOIN roles r ON r.id = u.role_id
		WHERE u.email_folded = ?`
	return scanUser(d.QueryRowContext(ctx, q, folded))
}

// UserByPhone looks up a user by the folded phone number.
func (d *DB) UserByPhone(ctx context.Context, folded string) (identity.User, error) {
	q := `SELECT ` + userColumns + `
		FROM users u JOIN roles r ON r.id = u.role_id
		WHERE u.phone_folded = ?`
	return scanUser(d.QueryRowContext(ctx, q, folded))
}

// IdentifierTaken reports whether any of the given folded identifiers already
// belongs to an account. All three namespaces share one namespace: a username
// may not equal another account's e-mail or phone, otherwise login would be
// ambiguous (docs/15-m5-plan.md §11). Empty arguments are ignored.
func (d *DB) IdentifierTaken(ctx context.Context, identifiers ...string) (bool, error) {
	values := make([]any, 0, len(identifiers))
	for _, id := range identifiers {
		if id != "" {
			values = append(values, id)
		}
	}
	if len(values) == 0 {
		return false, nil
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(values)), ",")
	q := `SELECT COUNT(*) FROM users u
		WHERE u.username_folded IN (` + placeholders + `)
		   OR u.email_folded IN (` + placeholders + `)
		   OR u.phone_folded IN (` + placeholders + `)`
	args := make([]any, 0, len(values)*3)
	for i := 0; i < 3; i++ {
		args = append(args, values...)
	}
	var n int
	if err := d.QueryRowContext(ctx, q, args...).Scan(&n); err != nil {
		return false, fmt.Errorf("sqlstore: identifier taken: %w", err)
	}
	return n > 0, nil
}

// UserByID loads one user.
func (d *DB) UserByID(ctx context.Context, id int64) (identity.User, error) {
	q := `SELECT ` + userColumns + `
		FROM users u JOIN roles r ON r.id = u.role_id
		WHERE u.id = ?`
	return scanUser(d.QueryRowContext(ctx, q, id))
}

func scanUser(row interface{ Scan(...any) error }) (identity.User, error) {
	var (
		u           identity.User
		lastSeen    sql.NullInt64
		lockedUntil sql.NullInt64
		pwChanged   sql.NullInt64
		email       sql.NullString
		emailFolded sql.NullString
		phone       sql.NullString
		phoneFolded sql.NullString
		isRoot      int
		canCreate   int
		created     int64
		updated     int64
		deletedAt   sql.NullInt64
		purgeAfter  sql.NullInt64
	)
	err := row.Scan(&u.ID, &u.Username, &u.UsernameFolded, &u.DisplayName, &u.PasswordHash,
		&u.RoleID, &u.RoleCode, &u.Status, &u.UsedBytes, &u.QuotaBytes, &u.ReservedBytes,
		&u.FailedLoginCount, &created, &updated, &lastSeen, &lockedUntil, &pwChanged,
		&email, &emailFolded, &phone, &phoneFolded, &isRoot, &canCreate, &deletedAt, &purgeAfter)
	if err != nil {
		if err == sql.ErrNoRows {
			return identity.User{}, apperr.ErrNotFound
		}
		return identity.User{}, fmt.Errorf("sqlstore: scan user: %w", err)
	}
	u.CreatedAt = time.Unix(created, 0).UTC()
	u.UpdatedAt = time.Unix(updated, 0).UTC()
	if lastSeen.Valid {
		t := time.Unix(lastSeen.Int64, 0).UTC()
		u.LastLoginAt = &t
	}
	if lockedUntil.Valid {
		t := time.Unix(lockedUntil.Int64, 0).UTC()
		u.LockedUntil = &t
	}
	if pwChanged.Valid {
		t := time.Unix(pwChanged.Int64, 0).UTC()
		u.PasswordChangedAt = &t
	}
	if deletedAt.Valid {
		t := time.Unix(deletedAt.Int64, 0).UTC()
		u.DeletedAt = &t
	}
	if purgeAfter.Valid {
		t := time.Unix(purgeAfter.Int64, 0).UTC()
		u.PurgeAfter = &t
	}
	u.Email = email.String
	u.EmailFolded = emailFolded.String
	u.Phone = phone.String
	u.PhoneFolded = phoneFolded.String
	u.IsRoot = isRoot != 0
	u.CanCreateGroups = canCreate != 0
	return u, nil
}

// CountUsers returns the number of accounts (used to decide bootstrap).
func (d *DB) CountUsers(ctx context.Context) (int, error) {
	var n int
	if err := d.QueryRowContext(ctx, `SELECT COUNT(*) FROM users`).Scan(&n); err != nil {
		return 0, fmt.Errorf("sqlstore: count users: %w", err)
	}
	return n, nil
}

// RoleIDByCode resolves a role code to its id.
func (d *DB) RoleIDByCode(ctx context.Context, code string) (int64, error) {
	var id int64
	err := d.QueryRowContext(ctx, `SELECT id FROM roles WHERE code = ?`, code).Scan(&id)
	if err == sql.ErrNoRows {
		return 0, fmt.Errorf("sqlstore: role %q missing (migrations not applied?)", code)
	}
	if err != nil {
		return 0, fmt.Errorf("sqlstore: role by code: %w", err)
	}
	return id, nil
}

// CreateUser inserts an account and returns it with the role code filled in.
// Optional identifiers are stored as NULL when empty so the partial unique
// indexes do not treat "no e-mail" as a duplicate.
func (d *DB) CreateUser(ctx context.Context, u identity.User) (identity.User, error) {
	now := time.Now().Unix()
	if u.Status == "" {
		u.Status = identity.StatusActive
	}
	isRoot := 0
	if u.IsRoot {
		isRoot = 1
	}
	res, err := d.ExecContext(ctx,
		`INSERT INTO users (username, username_folded, display_name, password_hash,
			role_id, status, used_bytes, quota_bytes, created_at, updated_at,
			email, email_folded, phone, phone_folded, is_root)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		u.Username, u.UsernameFolded, u.DisplayName, u.PasswordHash,
		u.RoleID, u.Status, u.UsedBytes, u.QuotaBytes, now, now,
		nullText(u.Email), nullText(u.EmailFolded), nullText(u.Phone), nullText(u.PhoneFolded), isRoot)
	if err != nil {
		return identity.User{}, fmt.Errorf("sqlstore: insert user: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return identity.User{}, fmt.Errorf("sqlstore: user id: %w", err)
	}
	return d.UserByID(ctx, id)
}

// nullText maps an unset optional text column to NULL.
func nullText(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// TouchLastLogin records a successful login time.
func (d *DB) TouchLastLogin(ctx context.Context, userID int64) error {
	now := time.Now().Unix()
	_, err := d.ExecContext(ctx,
		`UPDATE users SET last_login_at = ?, updated_at = ? WHERE id = ?`,
		now, now, userID)
	return err
}

// RecordLoginFailure bumps the failure counter and optionally sets a lockout
// window. It returns the new counter value.
func (d *DB) RecordLoginFailure(ctx context.Context, userID int64, count int, lockedUntil *time.Time) (int, error) {
	var lock sql.NullInt64
	if lockedUntil != nil {
		lock = sql.NullInt64{Int64: lockedUntil.Unix(), Valid: true}
	}
	res, err := d.ExecContext(ctx,
		`UPDATE users SET failed_login_count = ?, locked_until = ?, updated_at = ?
		 WHERE id = ?`,
		count, lock, time.Now().Unix(), userID)
	if err != nil {
		return 0, fmt.Errorf("sqlstore: record login failure: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return 0, apperr.ErrNotFound
	}
	return count, nil
}

// ClearLoginFailures resets the lockout state after a successful login.
func (d *DB) ClearLoginFailures(ctx context.Context, userID int64) error {
	_, err := d.ExecContext(ctx,
		`UPDATE users SET failed_login_count = 0, locked_until = NULL, updated_at = ?
		 WHERE id = ?`, time.Now().Unix(), userID)
	if err != nil {
		return fmt.Errorf("sqlstore: clear login failures: %w", err)
	}
	return nil
}

// UpdatePassword stores a new password hash and flips updated_at.
func (d *DB) UpdatePassword(ctx context.Context, userID int64, hash string, changedAt time.Time) error {
	_, err := d.ExecContext(ctx,
		`UPDATE users SET password_hash = ?, password_changed_at = ?, updated_at = ?
		 WHERE id = ?`,
		hash, changedAt.Unix(), changedAt.Unix(), userID)
	if err != nil {
		return fmt.Errorf("sqlstore: update password: %w", err)
	}
	return nil
}
