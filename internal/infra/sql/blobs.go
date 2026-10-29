package sqlstore

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"rapig/internal/platform/apperr"
)

// Blob is one content addressed object in the blobs table. refcount counts how
// many nodes point at it, so identical uploads are stored once and deleted
// only when the last node goes away. Since M5.3 a blob belongs to one storage
// policy: the same bytes uploaded into two roots produce two rows, one object
// file per root (dedup is per root, never across roots).
type Blob struct {
	ID              int64
	StoragePolicyID int64
	SHA256          string
	Size            int64
	Refcount        int
	CreatedAt       time.Time
}

const blobColumns = `id, storage_policy_id, sha256, size, refcount, created_at`

func scanBlob(row interface{ Scan(...any) error }) (Blob, error) {
	var (
		b       Blob
		created int64
	)
	err := row.Scan(&b.ID, &b.StoragePolicyID, &b.SHA256, &b.Size, &b.Refcount, &created)
	if err != nil {
		if err == sql.ErrNoRows {
			return Blob{}, apperr.ErrNotFound
		}
		return Blob{}, fmt.Errorf("sqlstore: scan blob: %w", err)
	}
	b.CreatedAt = unixUTC(created)
	return b, nil
}

// InsertBlob records a blob in the default policy; tests and legacy call sites use
// it when there is only one root. Production paths call InsertBlobInPolicy.
func (d *DB) InsertBlob(ctx context.Context, sha256Hex string, size int64) (Blob, bool, error) {
	p, err := d.DefaultStoragePolicy(ctx)
	if err != nil {
		return Blob{}, false, err
	}
	return d.InsertBlobInPolicy(ctx, p.ID, sha256Hex, size)
}

// InsertBlobInPolicy records a blob and its first reference inside one policy.
// When the (policy, hash) pair already exists it returns the existing row with
// created=false (client side dedup: the bytes do not need to be stored again).
// Callers that then fail to link a node must call DecRef to keep the refcount
// honest.
func (d *DB) InsertBlobInPolicy(ctx context.Context, policyID int64, sha256Hex string, size int64) (Blob, bool, error) {
	return d.insertBlob(ctx, policyID, sha256Hex, size)
}

func (d *DB) insertBlob(ctx context.Context, policyID int64, sha256Hex string, size int64) (Blob, bool, error) {
	now := time.Now().Unix()
	res, err := d.ExecContext(ctx,
		`INSERT INTO blobs (storage_policy_id, sha256, size, refcount, created_at)
		 VALUES (?, ?, ?, 1, ?)
		 ON CONFLICT(storage_policy_id, sha256) DO NOTHING`, policyID, sha256Hex, size, now)
	if err != nil {
		return Blob{}, false, fmt.Errorf("sqlstore: insert blob: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return Blob{}, false, fmt.Errorf("sqlstore: insert blob rows: %w", err)
	}
	if n == 0 {
		existing, err := d.BlobBySHAInPolicy(ctx, policyID, sha256Hex)
		return existing, false, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return Blob{}, false, fmt.Errorf("sqlstore: insert blob id: %w", err)
	}
	return Blob{
		ID:              id,
		StoragePolicyID: policyID,
		SHA256:          sha256Hex,
		Size:            size,
		Refcount:        1,
		CreatedAt:       unixUTC(now),
	}, true, nil
}

// BlobBySHA loads one blob from the default policy; tests and legacy call sites
// use it when only one root exists.
func (d *DB) BlobBySHA(ctx context.Context, sha256Hex string) (Blob, error) {
	p, err := d.DefaultStoragePolicy(ctx)
	if err != nil {
		return Blob{}, err
	}
	return d.BlobBySHAInPolicy(ctx, p.ID, sha256Hex)
}

// BlobBySHAInPolicy loads one blob inside a policy; unknown hashes are
// apperr.ErrNotFound so callers cannot distinguish "never uploaded" from
// "hidden by scope". Content addressed storage has no per-user scope: a hash
// only exists if some node in that policy references it.
func (d *DB) BlobBySHAInPolicy(ctx context.Context, policyID int64, sha256Hex string) (Blob, error) {
	return scanBlob(d.QueryRowContext(ctx,
		`SELECT `+blobColumns+` FROM blobs WHERE storage_policy_id = ? AND sha256 = ?`,
		policyID, sha256Hex))
}

// BlobByID loads one blob row by primary key (used by the admin/fsck paths,
// where the policy is not known up front).
func (d *DB) BlobByID(ctx context.Context, id int64) (Blob, error) {
	return scanBlob(d.QueryRowContext(ctx,
		`SELECT `+blobColumns+` FROM blobs WHERE id = ?`, id))
}

// IncRef adds one reference to a blob.
func (d *DB) IncRef(ctx context.Context, id int64) error {
	res, err := d.ExecContext(ctx,
		`UPDATE blobs SET refcount = refcount + 1 WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("sqlstore: inc blob ref: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("sqlstore: inc blob ref rows: %w", err)
	}
	if n == 0 {
		return apperr.ErrNotFound
	}
	return nil
}

// DecRef drops one reference and deletes the blob row when the count reaches
// zero, returning the remaining count (0 means deleted). Deduplicated content
// is only physically removed by the caller that gets 0 back.
func (d *DB) DecRef(ctx context.Context, id int64) (int, error) {
	var remaining int
	err := d.WithTx(ctx, func(tx *sql.Tx) error {
		var refcount int
		if err := tx.QueryRowContext(ctx,
			`SELECT refcount FROM blobs WHERE id = ?`, id).Scan(&refcount); err != nil {
			if err == sql.ErrNoRows {
				return apperr.ErrNotFound
			}
			return fmt.Errorf("sqlstore: select blob refcount: %w", err)
		}
		if refcount <= 1 {
			if _, err := tx.ExecContext(ctx, `DELETE FROM blobs WHERE id = ?`, id); err != nil {
				return fmt.Errorf("sqlstore: delete blob: %w", err)
			}
			remaining = 0
			return nil
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE blobs SET refcount = refcount - 1 WHERE id = ?`, id); err != nil {
			return fmt.Errorf("sqlstore: dec blob ref: %w", err)
		}
		remaining = refcount - 1
		return nil
	})
	if err != nil {
		return 0, err
	}
	return remaining, nil
}
