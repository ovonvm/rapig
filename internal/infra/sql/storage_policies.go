package sqlstore

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"rapig/internal/platform/apperr"
)

// StoragePolicy is one storage root. M5.3 allows several local policies; every
// policy is either enabled (new content may be written to it) or disabled (the
// files keep being served, nothing new lands there) and exactly one of them is
// the default for nodes created without an explicit policy.
type StoragePolicy struct {
	ID        int64
	Name      string
	Type      string
	RootPath  string
	Enabled   bool
	IsDefault bool
	UsedBytes int64
	CreatedAt time.Time
	UpdatedAt time.Time

	// NodeCount and FileCount are filled by ListStoragePolicies only. The
	// admin API shows them so an operator can see which root is in use before
	// removing it.
	NodeCount int64
	FileCount int64
}

const policyColumns = `p.id, p.name, p.type, p.root_path, p.enabled, p.is_default,
	p.used_bytes, p.created_at, p.updated_at`

// scanPolicyWithCounts scans policyColumns plus the two aggregate counts.
func scanPolicyWithCounts(row interface{ Scan(...any) error }) (StoragePolicy, error) {
	var (
		p         StoragePolicy
		enabled   int
		isDefault int
		created   int64
		updated   int64
	)
	if err := row.Scan(&p.ID, &p.Name, &p.Type, &p.RootPath, &enabled, &isDefault,
		&p.UsedBytes, &created, &updated, &p.NodeCount, &p.FileCount); err != nil {
		return StoragePolicy{}, err
	}
	p.Enabled = enabled != 0
	p.IsDefault = isDefault != 0
	p.CreatedAt = unixUTC(created)
	p.UpdatedAt = unixUTC(updated)
	return p, nil
}

func scanPolicy(row interface{ Scan(...any) error }) (StoragePolicy, error) {
	var (
		p         StoragePolicy
		enabled   int
		isDefault int
		created   int64
		updated   int64
	)
	if err := row.Scan(&p.ID, &p.Name, &p.Type, &p.RootPath, &enabled, &isDefault,
		&p.UsedBytes, &created, &updated); err != nil {
		return StoragePolicy{}, err
	}
	p.Enabled = enabled != 0
	p.IsDefault = isDefault != 0
	p.CreatedAt = unixUTC(created)
	p.UpdatedAt = unixUTC(updated)
	return p, nil
}

// ListStoragePolicies returns every policy with its node and file counts.
// includeDisabled=false is what the user facing endpoint needs; the admin view
// passes true.
func (d *DB) ListStoragePolicies(ctx context.Context, includeDisabled bool) ([]StoragePolicy, error) {
	include := 0
	if includeDisabled {
		include = 1
	}
	// used_bytes is derived from the blobs table instead of the denormalised
	// column: blob rows are created and removed by several code paths (upload,
	// copy, trash purge, gc) and keeping every one of them in step is easy to
	// miss. A live aggregate is exact, cheap at policy granularity and keeps the
	// admin view honest; the column stays as the value the last gc wrote.
	rows, err := d.QueryContext(ctx,
		`SELECT p.id, p.name, p.type, p.root_path, p.enabled, p.is_default,
		        COALESCE((SELECT SUM(b.size) FROM blobs b WHERE b.storage_policy_id = p.id), 0),
		        p.created_at, p.updated_at,
		        COUNT(n.id),
		        COALESCE(SUM(CASE WHEN n.kind = 'file' THEN 1 ELSE 0 END), 0)
		   FROM storage_policies p
		   LEFT JOIN nodes n ON n.storage_policy_id = p.id AND n.state != 'purged'
		  WHERE (? = 1 OR p.enabled = 1)
		  GROUP BY p.id
		  ORDER BY p.id ASC`, include)
	if err != nil {
		return nil, fmt.Errorf("sqlstore: list storage policies: %w", err)
	}
	defer rows.Close()
	out := make([]StoragePolicy, 0, 4)
	for rows.Next() {
		p, err := scanPolicyWithCounts(rows)
		if err != nil {
			return nil, fmt.Errorf("sqlstore: scan storage policy: %w", err)
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("sqlstore: storage policy rows: %w", err)
	}
	return out, nil
}

// StoragePolicyByID loads one policy; an unknown id is apperr.ErrNotFound.
func (d *DB) StoragePolicyByID(ctx context.Context, id int64) (StoragePolicy, error) {
	p, err := scanPolicy(d.QueryRowContext(ctx,
		`SELECT `+policyColumns+` FROM storage_policies p WHERE p.id = ?`, id))
	if err == sql.ErrNoRows {
		return StoragePolicy{}, apperr.ErrNotFound
	}
	if err != nil {
		return StoragePolicy{}, fmt.Errorf("sqlstore: select storage policy: %w", err)
	}
	return p, nil
}

// DefaultStoragePolicy returns the policy flagged is_default=1.
func (d *DB) DefaultStoragePolicy(ctx context.Context) (StoragePolicy, error) {
	p, err := scanPolicy(d.QueryRowContext(ctx,
		`SELECT `+policyColumns+` FROM storage_policies p WHERE p.is_default = 1`))
	if err == sql.ErrNoRows {
		return StoragePolicy{}, apperr.ErrNotFound
	}
	if err != nil {
		return StoragePolicy{}, fmt.Errorf("sqlstore: select default storage policy: %w", err)
	}
	return p, nil
}

// EnabledStoragePolicies lists the policies new content may be written to.
func (d *DB) EnabledStoragePolicies(ctx context.Context) ([]StoragePolicy, error) {
	rows, err := d.QueryContext(ctx,
		`SELECT `+policyColumns+` FROM storage_policies p WHERE p.enabled = 1 ORDER BY p.id ASC`)
	if err != nil {
		return nil, fmt.Errorf("sqlstore: list enabled storage policies: %w", err)
	}
	defer rows.Close()
	var out []StoragePolicy
	for rows.Next() {
		p, err := scanPolicy(rows)
		if err != nil {
			return nil, fmt.Errorf("sqlstore: scan enabled storage policy: %w", err)
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("sqlstore: enabled storage policy rows: %w", err)
	}
	return out, nil
}

// NewStoragePolicy is the create payload. Validated by the admin service, not
// here: the store only enforces uniqueness.
type NewStoragePolicy struct {
	Name      string
	RootPath  string
	Enabled   bool
	IsDefault bool
}

// CreateStoragePolicy inserts a policy. When is_default is set the other rows
// are cleared in the same transaction so the partial unique index always holds.
func (d *DB) CreateStoragePolicy(ctx context.Context, in NewStoragePolicy) (StoragePolicy, error) {
	var id int64
	err := d.WithTx(ctx, func(tx *sql.Tx) error {
		now := time.Now().Unix()
		enabled, isDefault := sqlBool(in.Enabled), sqlBool(in.IsDefault)
		if in.IsDefault {
			if _, err := tx.ExecContext(ctx,
				`UPDATE storage_policies SET is_default = 0 WHERE is_default = 1`); err != nil {
				return fmt.Errorf("sqlstore: clear default storage policy: %w", err)
			}
		}
		res, err := tx.ExecContext(ctx,
			`INSERT INTO storage_policies (name, type, root_path, enabled, is_default,
				used_bytes, created_at, updated_at)
			 VALUES (?, 'local', ?, ?, ?, 0, ?, ?)`,
			in.Name, in.RootPath, enabled, isDefault, now, now)
		if err != nil {
			if isUniqueViolation(err) {
				return apperr.ErrNameConflict
			}
			return fmt.Errorf("sqlstore: insert storage policy: %w", err)
		}
		id, err = res.LastInsertId()
		if err != nil {
			return fmt.Errorf("sqlstore: storage policy id: %w", err)
		}
		return nil
	})
	if err != nil {
		return StoragePolicy{}, err
	}
	return d.StoragePolicyByID(ctx, id)
}

// StoragePolicyUpdate carries the optional PATCH fields; nil means "unchanged".
type StoragePolicyUpdate struct {
	Name      *string
	RootPath  *string
	Enabled   *bool
	IsDefault *bool
}

// UpdateStoragePolicy applies a partial update. Business rules (root in use,
// absolute path, overlap) belong to the admin service; this method only owns
// the default flag invariant and the unique name.
func (d *DB) UpdateStoragePolicy(ctx context.Context, id int64, up StoragePolicyUpdate) (StoragePolicy, error) {
	err := d.WithTx(ctx, func(tx *sql.Tx) error {
		var exists int
		err := tx.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM storage_policies WHERE id = ?`, id).Scan(&exists)
		if err != nil {
			return fmt.Errorf("sqlstore: select storage policy: %w", err)
		}
		if exists == 0 {
			return apperr.ErrNotFound
		}
		if up.IsDefault != nil && *up.IsDefault {
			if _, err := tx.ExecContext(ctx,
				`UPDATE storage_policies SET is_default = 0 WHERE id != ?`, id); err != nil {
				return fmt.Errorf("sqlstore: clear default storage policy: %w", err)
			}
		}
		sets := make([]string, 0, 5)
		args := make([]any, 0, 6)
		if up.Name != nil {
			sets = append(sets, "name = ?")
			args = append(args, *up.Name)
		}
		if up.RootPath != nil {
			sets = append(sets, "root_path = ?")
			args = append(args, *up.RootPath)
		}
		if up.Enabled != nil {
			sets = append(sets, "enabled = ?")
			args = append(args, sqlBool(*up.Enabled))
		}
		if up.IsDefault != nil {
			sets = append(sets, "is_default = ?")
			args = append(args, sqlBool(*up.IsDefault))
		}
		if len(sets) == 0 {
			return nil
		}
		sets = append(sets, "updated_at = ?")
		args = append(args, time.Now().Unix(), id)
		res, err := tx.ExecContext(ctx,
			`UPDATE storage_policies SET `+strings.Join(sets, ", ")+` WHERE id = ?`, args...)
		if err != nil {
			if isUniqueViolation(err) {
				return apperr.ErrNameConflict
			}
			return fmt.Errorf("sqlstore: update storage policy: %w", err)
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return apperr.ErrNotFound
		}
		return nil
	})
	if err != nil {
		return StoragePolicy{}, err
	}
	return d.StoragePolicyByID(ctx, id)
}

// DeleteStoragePolicy removes a row. Callers must check usage first: the
// foreign keys from nodes/blobs/transfers make the delete fail otherwise.
func (d *DB) DeleteStoragePolicy(ctx context.Context, id int64) error {
	res, err := d.ExecContext(ctx, `DELETE FROM storage_policies WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("sqlstore: delete storage policy: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return apperr.ErrNotFound
	}
	return nil
}

// SetDefaultStoragePolicy makes id the only default in one transaction.
func (d *DB) SetDefaultStoragePolicy(ctx context.Context, id int64) error {
	return d.WithTx(ctx, func(tx *sql.Tx) error {
		var exists int
		if err := tx.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM storage_policies WHERE id = ?`, id).Scan(&exists); err != nil {
			return fmt.Errorf("sqlstore: select storage policy: %w", err)
		}
		if exists == 0 {
			return apperr.ErrNotFound
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE storage_policies SET is_default = 0 WHERE id != ?`, id); err != nil {
			return fmt.Errorf("sqlstore: clear default storage policy: %w", err)
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE storage_policies SET is_default = 1, updated_at = ? WHERE id = ?`,
			time.Now().Unix(), id); err != nil {
			return fmt.Errorf("sqlstore: set default storage policy: %w", err)
		}
		return nil
	})
}

// CountStoragePolicyUsage reports how many live nodes and blobs still point at
// a policy. It gates root_path changes and deletion.
func (d *DB) CountStoragePolicyUsage(ctx context.Context, id int64) (nodes, blobs int64, err error) {
	err = d.QueryRowContext(ctx,
		`SELECT (SELECT COUNT(*) FROM nodes WHERE storage_policy_id = ? AND state != 'purged'),
		        (SELECT COUNT(*) FROM blobs WHERE storage_policy_id = ?)`,
		id, id).Scan(&nodes, &blobs)
	if err != nil {
		return 0, 0, fmt.Errorf("sqlstore: count storage policy usage: %w", err)
	}
	return nodes, blobs, nil
}

// EnsureDefaultStoragePolicy returns the default local storage policy and
// creates it at rootPath when the table is empty. When policies exist but none
// is flagged default (databases written before M5.3), the lowest id wins.
func (d *DB) EnsureDefaultStoragePolicy(ctx context.Context, rootPath string) (int64, error) {
	var id int64
	err := d.QueryRowContext(ctx,
		`SELECT id FROM storage_policies WHERE is_default = 1`).Scan(&id)
	switch {
	case err == nil:
		return id, nil
	case err != sql.ErrNoRows:
		return 0, fmt.Errorf("sqlstore: select default storage policy: %w", err)
	}

	var lowest sql.NullInt64
	if err := d.QueryRowContext(ctx,
		`SELECT MIN(id) FROM storage_policies`).Scan(&lowest); err != nil {
		return 0, fmt.Errorf("sqlstore: select storage policy: %w", err)
	}
	if lowest.Valid {
		if _, err := d.ExecContext(ctx,
			`UPDATE storage_policies SET is_default = 1, updated_at = ? WHERE id = ?`,
			time.Now().Unix(), lowest.Int64); err != nil {
			return 0, fmt.Errorf("sqlstore: promote default storage policy: %w", err)
		}
		return lowest.Int64, nil
	}

	now := time.Now().Unix()
	res, err := d.ExecContext(ctx,
		`INSERT INTO storage_policies (name, type, root_path, enabled, is_default,
			used_bytes, created_at, updated_at)
		 VALUES ('local', 'local', ?, 1, 1, 0, ?, ?)`, rootPath, now, now)
	if err != nil {
		if isUniqueViolation(err) {
			return 0, fmt.Errorf("sqlstore: insert default storage policy: %w", err)
		}
		return 0, fmt.Errorf("sqlstore: insert storage policy: %w", err)
	}
	return res.LastInsertId()
}

func sqlBool(v bool) int {
	if v {
		return 1
	}
	return 0
}
