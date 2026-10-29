package sqlstore

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"rapig/internal/domain/access"
	"rapig/internal/domain/files"
	"rapig/internal/platform/apperr"
)

// nodeColumns is the canonical SELECT list for files.Node. Every query that
// feeds scanNode must use it (and include it in that order).
const nodeColumns = `id, owner_id, parent_id, kind, name, name_folded, path, blob_id,
	size, storage_policy_id, state, trashed_from, trashed_at, rev, created_at, updated_at,
	group_id`

// EnsureRoot returns the owner's root node, creating it when missing. The
// partial unique index nodes_one_root makes this safe under concurrency.
func (d *DB) EnsureRoot(ctx context.Context, ownerID, policyID int64) (files.Node, error) {
	q := `SELECT ` + nodeColumns + ` FROM nodes
		WHERE owner_id = ? AND parent_id IS NULL AND group_id IS NULL`
	n, err := scanNode(d.QueryRowContext(ctx, q, ownerID))
	if err == nil {
		return n, nil
	}
	if err != apperr.ErrNotFound {
		return files.Node{}, err
	}
	now := time.Now().Unix()
	_, err = d.ExecContext(ctx,
		`INSERT INTO nodes (owner_id, parent_id, kind, name, name_folded, path, size,
			storage_policy_id, state, created_at, updated_at)
		 VALUES (?, NULL, 'dir', '', '', '', 0, ?, 'active', ?, ?)`,
		ownerID, policyID, now, now)
	if err != nil {
		return files.Node{}, fmt.Errorf("sqlstore: insert root node: %w", err)
	}
	return scanNode(d.QueryRowContext(ctx, q, ownerID))
}

// NodeByID loads a node inside scope. A node owned by someone else is reported
// as apperr.ErrNotFound so callers cannot probe existence.
func (d *DB) NodeByID(ctx context.Context, sc access.Scope, id int64) (files.Node, error) {
	if err := sc.Validate(); err != nil {
		return files.Node{}, err
	}
	accessSQL, accessArgs := nodeAccessSQL(sc)
	q := `SELECT ` + nodeColumns + ` FROM nodes WHERE id = ? AND ` + accessSQL + ` AND state != 'purged'`
	args := append([]any{id}, accessArgs...)
	return scanNode(d.QueryRowContext(ctx, q, args...))
}

// ListChildren returns active children of parentID inside scope plus the
// total count. Ordering is directories first, then case folded name.
func (d *DB) ListChildren(ctx context.Context, sc access.Scope, parentID int64, limit, offset int) ([]files.Node, int, error) {
	if err := sc.Validate(); err != nil {
		return nil, 0, err
	}
	accessSQL, accessArgs := nodeAccessSQL(sc)
	countArgs := append([]any{parentID}, accessArgs...)
	var total int
	if err := d.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM nodes
		 WHERE parent_id = ? AND `+accessSQL+` AND state = 'active'`,
		countArgs...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("sqlstore: count children: %w", err)
	}
	listArgs := append([]any{parentID}, accessArgs...)
	listArgs = append(listArgs, limit, offset)
	rows, err := d.QueryContext(ctx,
		`SELECT `+nodeColumns+`
		 FROM nodes WHERE parent_id = ? AND `+accessSQL+` AND state = 'active'
		 ORDER BY CASE kind WHEN 'dir' THEN 0 ELSE 1 END, name_folded ASC, id ASC
		 LIMIT ? OFFSET ?`, listArgs...)
	if err != nil {
		return nil, 0, fmt.Errorf("sqlstore: list children: %w", err)
	}
	defer rows.Close()
	out := make([]files.Node, 0, limit)
	for rows.Next() {
		n, err := scanNode(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, n)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("sqlstore: children rows: %w", err)
	}
	return out, total, nil
}

// CreateDir inserts a directory that inherits its parent policy. The policy is
// resolved in the same statement, so a child can never land in a different root
// than its parent (M5.3: the tree is policy-uniform per subtree).
func (d *DB) CreateDir(ctx context.Context, sc access.Scope, parentID int64, name, relPath string) (files.Node, error) {
	if err := sc.Validate(); err != nil {
		return files.Node{}, err
	}
	return d.insertDir(ctx, sc, parentID, name, relPath,
		`COALESCE(
			(SELECT p.storage_policy_id FROM nodes p WHERE p.id = ?),
			(SELECT id FROM storage_policies WHERE is_default = 1))`, parentID)
}

// CreateDirInPolicy inserts a directory under an explicit policy. Only the file
// service uses it, for a folder created directly under the account root.
func (d *DB) CreateDirInPolicy(ctx context.Context, sc access.Scope, parentID int64, name, relPath string, policyID int64) (files.Node, error) {
	if err := sc.Validate(); err != nil {
		return files.Node{}, err
	}
	return d.insertDir(ctx, sc, parentID, name, relPath, `?`, policyID)
}

// insertDir builds the policy expression and its bind arguments separately: the
// inherited form binds the parent id twice (once for the parent_id column, once
// inside the COALESCE subquery), the explicit form binds the policy once.
func (d *DB) insertDir(ctx context.Context, sc access.Scope, parentID int64, name, relPath, policyExpr string, policyArgs ...any) (files.Node, error) {
	if err := sc.Validate(); err != nil {
		return files.Node{}, err
	}
	now := time.Now().Unix()
	args := []any{sc.UserID, parentID, name, files.FoldName(name), relPath, parentID}
	args = append(args, policyArgs...)
	args = append(args, now, now)
	res, err := d.ExecContext(ctx,
		`INSERT INTO nodes (owner_id, parent_id, kind, name, name_folded, path,
			group_id, storage_policy_id, state, created_at, updated_at)
		 VALUES (?, ?, 'dir', ?, ?, ?,
			(SELECT p.group_id FROM nodes p WHERE p.id = ?), `+policyExpr+`, 'active', ?, ?)`, args...)
	if err != nil {
		if isUniqueViolation(err) {
			return files.Node{}, apperr.ErrNameConflict
		}
		if isForeignKeyViolation(err) {
			return files.Node{}, apperr.ErrParentNotFound
		}
		return files.Node{}, fmt.Errorf("sqlstore: insert dir: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return files.Node{}, fmt.Errorf("sqlstore: dir id: %w", err)
	}
	return d.NodeByID(ctx, sc, id)
}

func scanNode(row interface{ Scan(...any) error }) (files.Node, error) {
	var (
		n           files.Node
		parent      sql.NullInt64
		blob        sql.NullInt64
		policy      sql.NullInt64
		trashedFrom sql.NullInt64
		trashedAt   sql.NullInt64
		groupID     sql.NullInt64
		created     int64
		updated     int64
		kindText    string
		stateText   string
	)
	err := row.Scan(&n.ID, &n.OwnerID, &parent, &kindText, &n.Name, &n.NameFolded,
		&n.Path, &blob, &n.Size, &policy, &stateText, &trashedFrom, &trashedAt, &n.Rev, &created, &updated,
		&groupID)
	if err != nil {
		if err == sql.ErrNoRows {
			return files.Node{}, apperr.ErrNotFound
		}
		return files.Node{}, fmt.Errorf("sqlstore: scan node: %w", err)
	}
	if parent.Valid {
		v := parent.Int64
		n.ParentID = &v
	}
	if blob.Valid {
		v := blob.Int64
		n.BlobID = &v
	}
	if policy.Valid {
		v := policy.Int64
		n.StoragePolicyID = &v
	}
	if trashedFrom.Valid {
		v := trashedFrom.Int64
		n.TrashedFrom = &v
	}
	if trashedAt.Valid {
		t := time.Unix(trashedAt.Int64, 0).UTC()
		n.TrashedAt = &t
	}
	if groupID.Valid {
		v := groupID.Int64
		n.GroupID = &v
	}
	n.Kind = files.Kind(kindText)
	n.State = stateText
	n.CreatedAt = time.Unix(created, 0).UTC()
	n.UpdatedAt = time.Unix(updated, 0).UTC()
	return n, nil
}

func isUniqueViolation(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "unique constraint")
}

func isForeignKeyViolation(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "foreign key constraint")
}
