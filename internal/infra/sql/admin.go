package sqlstore

import (
	"context"
	"fmt"
	"time"
)

// OrphanBlobs returns blobs without any node reference older than cutoff. gc
// only touches these, so a blob that was just created and not yet committed is
// never deleted.
func (d *DB) OrphanBlobs(ctx context.Context, cutoff time.Time) ([]Blob, error) {
	return d.queryBlobs(ctx,
		`SELECT `+blobColumns+` FROM blobs
		 WHERE refcount = 0 AND created_at < ? ORDER BY id`, cutoff.Unix())
}

// OrphanBlobsByPolicy is OrphanBlobs restricted to one storage policy, which is
// what the per-policy gc pass uses: an orphan in root A must not make gc touch
// root B, and a disabled policy still needs its orphans collected.
func (d *DB) OrphanBlobsByPolicy(ctx context.Context, policyID int64, cutoff time.Time) ([]Blob, error) {
	return d.queryBlobs(ctx,
		`SELECT `+blobColumns+` FROM blobs
		 WHERE storage_policy_id = ? AND refcount = 0 AND created_at < ? ORDER BY id`,
		policyID, cutoff.Unix())
}

// DeleteBlobRow removes one blob row (gc, after the file is gone).
func (d *DB) DeleteBlobRow(ctx context.Context, id int64) error {
	if _, err := d.ExecContext(ctx, `DELETE FROM blobs WHERE id = ? AND refcount = 0`, id); err != nil {
		return fmt.Errorf("sqlstore: delete blob row: %w", err)
	}
	return nil
}

// AllBlobs returns every blob for fsck.
func (d *DB) AllBlobs(ctx context.Context) ([]Blob, error) {
	return d.queryBlobs(ctx, `SELECT `+blobColumns+` FROM blobs ORDER BY id`)
}

// BlobsByPolicy returns the blobs of one policy for the per-policy fsck pass.
func (d *DB) BlobsByPolicy(ctx context.Context, policyID int64) ([]Blob, error) {
	return d.queryBlobs(ctx,
		`SELECT `+blobColumns+` FROM blobs WHERE storage_policy_id = ? ORDER BY id`, policyID)
}

func (d *DB) queryBlobs(ctx context.Context, query string, args ...any) ([]Blob, error) {
	rows, err := d.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("sqlstore: select blobs: %w", err)
	}
	defer rows.Close()
	var out []Blob
	for rows.Next() {
		b, err := scanBlob(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("sqlstore: blob rows: %w", err)
	}
	return out, nil
}

// BlobNodeCounts counts live node references per blob. Purged rows are gone, so
// every remaining node counts.
func (d *DB) BlobNodeCounts(ctx context.Context) (map[int64]int64, error) {
	rows, err := d.QueryContext(ctx,
		`SELECT blob_id, COUNT(*) FROM nodes WHERE blob_id IS NOT NULL GROUP BY blob_id`)
	if err != nil {
		return nil, fmt.Errorf("sqlstore: count blob refs: %w", err)
	}
	defer rows.Close()
	out := make(map[int64]int64)
	for rows.Next() {
		var id, n int64
		if err := rows.Scan(&id, &n); err != nil {
			return nil, fmt.Errorf("sqlstore: scan blob ref count: %w", err)
		}
		out[id] = n
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("sqlstore: blob ref rows: %w", err)
	}
	return out, nil
}

// SetBlobRefcount writes a repaired refcount.
func (d *DB) SetBlobRefcount(ctx context.Context, id, refcount int64) error {
	if _, err := d.ExecContext(ctx, `UPDATE blobs SET refcount = ? WHERE id = ?`, refcount, id); err != nil {
		return fmt.Errorf("sqlstore: update blob refcount: %w", err)
	}
	return nil
}

// NodesWithMissingBlob returns node ids that reference a blob row that does not
// exist (foreign keys make this a sign of manual tampering).
func (d *DB) NodesWithMissingBlob(ctx context.Context) ([]int64, error) {
	rows, err := d.QueryContext(ctx,
		`SELECT n.id FROM nodes n LEFT JOIN blobs b ON b.id = n.blob_id
		 WHERE n.blob_id IS NOT NULL AND b.id IS NULL ORDER BY n.id`)
	if err != nil {
		return nil, fmt.Errorf("sqlstore: select broken node blobs: %w", err)
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("sqlstore: scan broken node: %w", err)
		}
		out = append(out, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("sqlstore: broken node rows: %w", err)
	}
	return out, nil
}

// NodesWithForeignBlob returns node ids whose blob belongs to another storage
// policy. The tree keeps the policy on the node, so a mismatch means the blob
// would be read from the wrong root.
func (d *DB) NodesWithForeignBlob(ctx context.Context) ([]int64, error) {
	rows, err := d.QueryContext(ctx,
		`SELECT n.id FROM nodes n JOIN blobs b ON b.id = n.blob_id
		 WHERE n.storage_policy_id IS NOT NULL AND b.storage_policy_id != n.storage_policy_id
		 ORDER BY n.id`)
	if err != nil {
		return nil, fmt.Errorf("sqlstore: select foreign node blobs: %w", err)
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("sqlstore: scan foreign node: %w", err)
		}
		out = append(out, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("sqlstore: foreign node rows: %w", err)
	}
	return out, nil
}
