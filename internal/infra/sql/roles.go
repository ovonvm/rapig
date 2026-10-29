package sqlstore

import (
	"context"
	"fmt"

	"rapig/internal/domain/access"
)

// PermissionsForRole loads the permission set of a role.
func (d *DB) PermissionsForRole(ctx context.Context, roleID int64) (access.PermSet, error) {
	rows, err := d.QueryContext(ctx,
		`SELECT p.code FROM role_permissions rp
		 JOIN permissions p ON p.id = rp.permission_id
		 WHERE rp.role_id = ?`, roleID)
	if err != nil {
		return nil, fmt.Errorf("sqlstore: role permissions: %w", err)
	}
	defer rows.Close()

	set := make(access.PermSet)
	for rows.Next() {
		var code string
		if err := rows.Scan(&code); err != nil {
			return nil, fmt.Errorf("sqlstore: scan permission: %w", err)
		}
		if a, ok := access.ParseAction(code); ok {
			set[a] = true
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("sqlstore: role permissions rows: %w", err)
	}
	return set, nil
}
