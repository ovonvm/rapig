package sqlstore

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"rapig/internal/domain/access"
	"rapig/internal/domain/files"
	"rapig/internal/platform/apperr"
)

// CreateUpload inserts a new resumable upload session. The reserved quota is
// taken by the caller before this call (ReserveBytes) so a failed insert must
// release it. The session remembers the storage policy resolved from its parent
// so later parts and the completion land in the same root.
func (d *DB) CreateUpload(ctx context.Context, sc access.Scope, up files.Upload) error {
	if err := sc.Validate(); err != nil {
		return err
	}
	_, err := d.ExecContext(ctx,
		`INSERT INTO uploads (id, owner_id, parent_id, storage_policy_id, name, name_folded, size, sha256,
			part_size, parts_total, received_bytes, state, node_id, created_at, expires_at)
		 VALUES (?, ?, ?,
			COALESCE(NULLIF(?, 0), (SELECT p.storage_policy_id FROM nodes p WHERE p.id = ?),
				(SELECT id FROM storage_policies WHERE is_default = 1)),
			?, ?, ?, ?, ?, ?, 0, 'open', NULL, ?, ?)`,
		up.ID, sc.UserID, up.ParentID, up.StoragePolicyID, up.ParentID, up.Name, up.NameFolded, up.Size, up.SHA256,
		up.PartSize, up.PartsTotal, up.CreatedAt.Unix(), up.ExpiresAt.Unix())
	if err != nil {
		if isForeignKeyViolation(err) {
			return apperr.ErrParentNotFound
		}
		return fmt.Errorf("sqlstore: insert upload: %w", err)
	}
	return nil
}

const uploadColumns = `id, owner_id, parent_id, storage_policy_id, name, name_folded, size, sha256,
	part_size, parts_total, received_bytes, state, node_id, created_at, expires_at`

// UploadByID loads one upload session inside scope.
func (d *DB) UploadByID(ctx context.Context, sc access.Scope, id string) (files.Upload, error) {
	if err := sc.Validate(); err != nil {
		return files.Upload{}, err
	}
	row := d.QueryRowContext(ctx,
		`SELECT `+uploadColumns+` FROM uploads WHERE id = ? AND owner_id = ?`,
		id, sc.UserID)
	return scanUpload(row)
}

// UpsertPart records one part and recomputes the received byte counter. The
// upload must still be open; a repeated PUT for the same part overwrites it
// (idempotent retry after a flaky connection).
func (d *DB) UpsertPart(ctx context.Context, sc access.Scope, uploadID string, partNo, offset, size int64, sha256Hex string) error {
	if err := sc.Validate(); err != nil {
		return err
	}
	return d.WithTx(ctx, func(tx *sql.Tx) error {
		var state string
		err := tx.QueryRowContext(ctx,
			`SELECT state FROM uploads WHERE id = ? AND owner_id = ?`,
			uploadID, sc.UserID).Scan(&state)
		if err == sql.ErrNoRows {
			return apperr.ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("sqlstore: select upload state: %w", err)
		}
		if files.UploadState(state) != files.UploadOpen {
			return apperr.ErrInvalidState
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO upload_parts (upload_id, part_no, offset, size, sha256)
			 VALUES (?, ?, ?, ?, ?)
			 ON CONFLICT(upload_id, part_no) DO UPDATE SET
			   offset = excluded.offset, size = excluded.size, sha256 = excluded.sha256`,
			uploadID, partNo, offset, size, sha256Hex); err != nil {
			return fmt.Errorf("sqlstore: upsert upload part: %w", err)
		}
		_, err = tx.ExecContext(ctx,
			`UPDATE uploads SET received_bytes =
			   (SELECT COALESCE(SUM(size), 0) FROM upload_parts WHERE upload_id = ?)
			 WHERE id = ?`, uploadID, uploadID)
		if err != nil {
			return fmt.Errorf("sqlstore: update received bytes: %w", err)
		}
		return nil
	})
}

// UploadParts lists stored part rows ordered by part number.
func (d *DB) UploadParts(ctx context.Context, sc access.Scope, uploadID string) ([]files.UploadPart, error) {
	if err := sc.Validate(); err != nil {
		return nil, err
	}
	// The owner check is folded into the query so a foreign upload id simply
	// yields no parts.
	rows, err := d.QueryContext(ctx,
		`SELECT p.part_no, p.offset, p.size, p.sha256
		 FROM upload_parts p JOIN uploads u ON u.id = p.upload_id
		 WHERE p.upload_id = ? AND u.owner_id = ?
		 ORDER BY p.part_no ASC`, uploadID, sc.UserID)
	if err != nil {
		return nil, fmt.Errorf("sqlstore: list upload parts: %w", err)
	}
	defer rows.Close()
	var out []files.UploadPart
	for rows.Next() {
		var p files.UploadPart
		if err := rows.Scan(&p.PartNo, &p.Offset, &p.Size, &p.SHA256); err != nil {
			return nil, fmt.Errorf("sqlstore: scan upload part: %w", err)
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("sqlstore: upload parts rows: %w", err)
	}
	return out, nil
}

// MarkUploadCanceled flips an open upload to canceled; an already closed
// session is not an error (cancel is idempotent for the client).
func (d *DB) MarkUploadCanceled(ctx context.Context, sc access.Scope, uploadID string) error {
	if err := sc.Validate(); err != nil {
		return err
	}
	_, err := d.ExecContext(ctx,
		`UPDATE uploads SET state = 'canceled'
		 WHERE id = ? AND owner_id = ? AND state = 'open'`, uploadID, sc.UserID)
	if err != nil {
		return fmt.Errorf("sqlstore: cancel upload: %w", err)
	}
	return nil
}

// DeleteUpload removes a session row (cascades parts). Used by maintenance
// once an expired session has been cleaned up.
func (d *DB) DeleteUpload(ctx context.Context, sc access.Scope, uploadID string) error {
	if err := sc.Validate(); err != nil {
		return err
	}
	_, err := d.ExecContext(ctx,
		`DELETE FROM uploads WHERE id = ? AND owner_id = ?`, uploadID, sc.UserID)
	if err != nil {
		return fmt.Errorf("sqlstore: delete upload: %w", err)
	}
	return nil
}

// DeleteExpiredUpload removes an expired row without a user scope; only the
// maintenance path uses it.
func (d *DB) DeleteExpiredUpload(ctx context.Context, uploadID string) error {
	_, err := d.ExecContext(ctx, `DELETE FROM uploads WHERE id = ?`, uploadID)
	if err != nil {
		return fmt.Errorf("sqlstore: delete expired upload: %w", err)
	}
	return nil
}

// ExpiredUploads lists open sessions past their deadline, oldest first. It is
// system scoped: maintenance runs without a user principal.
func (d *DB) ExpiredUploads(ctx context.Context, now time.Time, limit int) ([]files.Upload, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := d.QueryContext(ctx,
		`SELECT `+uploadColumns+` FROM uploads
		 WHERE state = 'open' AND expires_at < ? ORDER BY expires_at ASC LIMIT ?`,
		now.Unix(), limit)
	if err != nil {
		return nil, fmt.Errorf("sqlstore: list expired uploads: %w", err)
	}
	defer rows.Close()
	var out []files.Upload
	for rows.Next() {
		u, err := scanUpload(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("sqlstore: expired uploads rows: %w", err)
	}
	return out, nil
}

// CompletedUploadInput describes the node to create when an upload finishes.
// UploadID is empty for the instant-upload (dedup) path, which has no session.
type CompletedUploadInput struct {
	// UploadID, when set, is closed in the same transaction.
	UploadID string
	ParentID int64
	// StoragePolicyID is the policy the new node belongs to. It must match the
	// blob row identified by BlobID.
	StoragePolicyID int64
	Name            string
	Path            string
	Mime            string
	SHA256          string
	Size            int64
	BlobID          int64
	// IncrementRef is true when the blob row already existed (dedup); when
	// InsertBlob created it the row already counts this reference.
	IncrementRef bool
	CreatedAt    time.Time
}

// CommitCompletedUpload creates the file node, moves/consumes quota and (when
// an upload session exists) closes it in one transaction. It is the only place
// where those invariants change together.
func (d *DB) CommitCompletedUpload(ctx context.Context, sc access.Scope, in CompletedUploadInput) (files.Node, error) {
	if err := sc.Validate(); err != nil {
		return files.Node{}, err
	}
	var nodeID int64
	err := d.WithTx(ctx, func(tx *sql.Tx) error {
		now := in.CreatedAt.Unix()
		res, err := tx.ExecContext(ctx,
			`INSERT INTO nodes (owner_id, parent_id, kind, name, name_folded, path,
				group_id, blob_id, size, storage_policy_id, mime, state, created_at, updated_at)
			 VALUES (?, ?, 'file', ?, ?, ?,
				(SELECT p.group_id FROM nodes p WHERE p.id = ?), ?, ?,
				COALESCE(NULLIF(?, 0), (SELECT p.storage_policy_id FROM nodes p WHERE p.id = ?),
					(SELECT id FROM storage_policies WHERE is_default = 1)),
				?, 'active', ?, ?)`,
			sc.UserID, in.ParentID, in.Name, files.FoldName(in.Name), in.Path,
			in.ParentID, in.BlobID, in.Size, in.StoragePolicyID, in.ParentID, in.Mime, now, now)
		if err != nil {
			if isUniqueViolation(err) {
				return apperr.ErrNameConflict
			}
			if isForeignKeyViolation(err) {
				return apperr.ErrParentNotFound
			}
			return fmt.Errorf("sqlstore: insert file node: %w", err)
		}
		nodeID, err = res.LastInsertId()
		if err != nil {
			return fmt.Errorf("sqlstore: file node id: %w", err)
		}
		if in.UploadID != "" {
			// Reserved bytes were granted at init; just move them.
			if _, err := tx.ExecContext(ctx,
				`UPDATE users SET
				   used_bytes = used_bytes + ?,
				   reserved_bytes = CASE WHEN reserved_bytes >= ? THEN reserved_bytes - ? ELSE 0 END,
				   updated_at = ?
				 WHERE id = ?`,
				in.Size, in.Size, in.Size, now, sc.UserID); err != nil {
				return fmt.Errorf("sqlstore: move reserved bytes: %w", err)
			}
		} else {
			// No reservation: charge now, refusing to exceed the quota.
			res, err := tx.ExecContext(ctx,
				`UPDATE users SET used_bytes = used_bytes + ?, updated_at = ?
				 WHERE id = ?
				   AND (quota_bytes = 0 OR used_bytes + reserved_bytes + ? <= quota_bytes)`,
				in.Size, now, sc.UserID, in.Size)
			if err != nil {
				return fmt.Errorf("sqlstore: consume bytes: %w", err)
			}
			if n, _ := res.RowsAffected(); n == 0 {
				return apperr.ErrQuotaExceeded.WithReason(apperr.ReasonQuotaExceeded)
			}
		}
		if in.IncrementRef {
			if _, err := tx.ExecContext(ctx,
				`UPDATE blobs SET refcount = refcount + 1 WHERE id = ?`, in.BlobID); err != nil {
				return fmt.Errorf("sqlstore: increment blob ref: %w", err)
			}
		}
		if in.UploadID != "" {
			res, err = tx.ExecContext(ctx,
				`UPDATE uploads SET state = 'completed', node_id = ?
				 WHERE id = ? AND owner_id = ? AND state = 'open'`,
				nodeID, in.UploadID, sc.UserID)
			if err != nil {
				return fmt.Errorf("sqlstore: complete upload row: %w", err)
			}
			if n, _ := res.RowsAffected(); n == 0 {
				return apperr.ErrInvalidState
			}
		}
		return nil
	})
	if err != nil {
		return files.Node{}, err
	}
	return d.NodeByID(ctx, sc, nodeID)
}

func unixUTC(sec int64) time.Time { return time.Unix(sec, 0).UTC() }

func scanUpload(row interface{ Scan(...any) error }) (files.Upload, error) {
	var (
		u       files.Upload
		policy  sql.NullInt64
		state   string
		nodeID  sql.NullInt64
		created int64
		expires int64
	)
	err := row.Scan(&u.ID, &u.OwnerID, &u.ParentID, &policy, &u.Name, &u.NameFolded, &u.Size,
		&u.SHA256, &u.PartSize, &u.PartsTotal, &u.ReceivedBytes, &state, &nodeID,
		&created, &expires)
	if err != nil {
		if err == sql.ErrNoRows {
			return files.Upload{}, apperr.ErrNotFound
		}
		return files.Upload{}, fmt.Errorf("sqlstore: scan upload: %w", err)
	}
	if policy.Valid {
		u.StoragePolicyID = policy.Int64
	}
	u.State = files.UploadState(state)
	if nodeID.Valid {
		v := nodeID.Int64
		u.NodeID = &v
	}
	u.CreatedAt = time.Unix(created, 0).UTC()
	u.ExpiresAt = time.Unix(expires, 0).UTC()
	return u, nil
}
