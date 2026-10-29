package sqlstore

import (
	"context"
	"fmt"
	"strings"
	"time"

	"rapig/internal/domain/identity"
	"rapig/internal/platform/apperr"
)

// ListUsers pages through accounts for the admin user page. q matches the
// folded username, the display name, the e-mail or the phone number
// (case-insensitive substring).
func (d *DB) ListUsers(ctx context.Context, q string, limit, offset int) ([]identity.User, int, error) {
	where := ""
	args := make([]any, 0, 8)
	if q != "" {
		like := "%" + strings.ToLower(q) + "%"
		where = ` WHERE (u.username_folded LIKE ? OR u.display_name LIKE ?
			OR u.email LIKE ? OR u.email_folded LIKE ?
			OR u.phone LIKE ? OR u.phone_folded LIKE ?)`
		args = append(args, like, like, like, like, like, like)
	}
	var total int
	if err := d.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM users u`+where, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("sqlstore: count users: %w", err)
	}
	rows, err := d.QueryContext(ctx,
		`SELECT `+userColumns+` FROM users u JOIN roles r ON r.id = u.role_id`+where+
			` ORDER BY u.created_at ASC, u.id ASC LIMIT ? OFFSET ?`,
		append(args, limit, offset)...)
	if err != nil {
		return nil, 0, fmt.Errorf("sqlstore: list users: %w", err)
	}
	defer rows.Close()
	out := make([]identity.User, 0, limit)
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, u)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("sqlstore: user rows: %w", err)
	}
	return out, total, nil
}

// UpdateUser applies a partial admin update. Nil fields are left untouched.
func (d *DB) UpdateUser(ctx context.Context, id int64, status *string, quota *int64, roleID *int64, canCreateGroups *bool) error {
	sets := make([]string, 0, 5)
	args := make([]any, 0, 6)
	if status != nil {
		sets = append(sets, "status = ?")
		args = append(args, *status)
	}
	if quota != nil {
		sets = append(sets, "quota_bytes = ?")
		args = append(args, *quota)
	}
	if roleID != nil {
		sets = append(sets, "role_id = ?")
		args = append(args, *roleID)
	}
	if canCreateGroups != nil {
		flag := 0
		if *canCreateGroups {
			flag = 1
		}
		sets = append(sets, "can_create_groups = ?")
		args = append(args, flag)
	}
	if len(sets) == 0 {
		return apperr.ErrBadRequest
	}
	sets = append(sets, "updated_at = ?")
	args = append(args, time.Now().Unix(), id)
	res, err := d.ExecContext(ctx,
		`UPDATE users SET `+strings.Join(sets, ", ")+` WHERE id = ?`, args...)
	if err != nil {
		return fmt.Errorf("sqlstore: update user: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return apperr.ErrNotFound
	}
	return nil
}

// CountActiveAdmins counts accounts that can still administer the instance.
func (d *DB) CountActiveAdmins(ctx context.Context) (int, error) {
	var n int
	err := d.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM users u JOIN roles r ON r.id = u.role_id
		WHERE r.code = 'admin' AND u.status = 'active'`).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("sqlstore: count admins: %w", err)
	}
	return n, nil
}

// ResetUserPassword replaces a password hash and revokes every existing
// session for that account (all session ids are stored hashed).
func (d *DB) ResetUserPassword(ctx context.Context, id int64, hash string, changedAt time.Time) error {
	res, err := d.ExecContext(ctx,
		`UPDATE users SET password_hash = ?, password_changed_at = ?, failed_login_count = 0,
			locked_until = NULL, updated_at = ? WHERE id = ?`,
		hash, changedAt.Unix(), changedAt.Unix(), id)
	if err != nil {
		return fmt.Errorf("sqlstore: reset password: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return apperr.ErrNotFound
	}
	if _, err := d.DeleteUserSessionsExcept(ctx, id, ""); err != nil {
		return err
	}
	return nil
}
