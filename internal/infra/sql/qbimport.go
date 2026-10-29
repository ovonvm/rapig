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

// QBImportEntry is one file below the imported top directory. Segs is the
// relative path inside the torrent (at least one element) and is validated by
// the caller; the sqlstore only enforces naming/state invariants.
type QBImportEntry struct {
	Segs         []string
	Size         int64
	BlobID       int64
	IncrementRef bool
	Mime         string
}

// QBImportInput describes one atomic multi-file torrent import: either a
// top-level directory with entries below it, or (TopIsFile) a single file.
// TopName must already be resolved against name conflicts by the caller.
type QBImportInput struct {
	TransferID      string
	ParentID        int64
	StoragePolicyID int64
	TopName         string
	TopIsFile       bool
	TopSize         int64
	TopBlobID       int64
	TopIncrementRef bool
	TopMime         string
	Entries         []QBImportEntry
	Now             time.Time
}

// CommitQBImport creates the imported node tree, charges quota, adjusts blob
// refcounts and marks the transfer completed in one transaction. Any failure
// rolls back the whole import; blobs copied before the call stay unreferenced
// and are reclaimed by the caller (or gc) when their refcount drops to zero.
func (d *DB) CommitQBImport(ctx context.Context, sc access.Scope, in QBImportInput) (files.Node, error) {
	if err := sc.Validate(); err != nil {
		return files.Node{}, err
	}
	if in.TopName == "" {
		return files.Node{}, apperr.ErrInvalidName
	}
	now := in.Now.Unix()

	var topID int64
	err := d.WithTx(ctx, func(tx *sql.Tx) error {
		// Resolve the parent row inside the transaction: the caller already
		// authorized it, but its path/policy must be read consistently.
		var (
			parentPath   string
			parentPolicy sql.NullInt64
			parentGroup  sql.NullInt64
		)
		q := `SELECT path, storage_policy_id, group_id FROM nodes
		      WHERE owner_id = ? AND state = 'active' AND id = ?`
		args := []any{sc.UserID, in.ParentID}
		if in.ParentID == 0 {
			q = `SELECT path, storage_policy_id, group_id FROM nodes
			     WHERE owner_id = ? AND state = 'active' AND parent_id IS NULL`
			args = []any{sc.UserID}
		}
		if err := tx.QueryRowContext(ctx, q, args...).Scan(&parentPath, &parentPolicy, &parentGroup); err != nil {
			if err == sql.ErrNoRows {
				return apperr.ErrParentNotFound
			}
			return fmt.Errorf("sqlstore: qb import parent: %w", err)
		}
		policy := in.StoragePolicyID
		if policy == 0 {
			if parentPolicy.Valid {
				policy = parentPolicy.Int64
			}
		}
		if policy == 0 {
			if err := tx.QueryRowContext(ctx,
				`SELECT id FROM storage_policies WHERE is_default = 1`).Scan(&policy); err != nil {
				return fmt.Errorf("sqlstore: qb import default policy: %w", err)
			}
		}
		var group any
		if parentGroup.Valid {
			group = parentGroup.Int64
		}

		insertNode := func(parentID int64, kind, name, path string, blobID *int64, size int64, mime string) (int64, error) {
			var blob any
			if blobID != nil {
				blob = *blobID
			}
			g := group
			res, err := tx.ExecContext(ctx,
				`INSERT INTO nodes (owner_id, parent_id, kind, name, name_folded, path,
					group_id, blob_id, size, storage_policy_id, mime, state, created_at, updated_at)
				 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'active', ?, ?)`,
				sc.UserID, nullableID(parentID), kind, name, files.FoldName(name), path,
				g, blob, size, policy, mime, now, now)
			if err != nil {
				if isUniqueViolation(err) {
					return 0, apperr.ErrNameConflict.WithCause(err)
				}
				if isForeignKeyViolation(err) {
					return 0, apperr.ErrParentNotFound
				}
				return 0, fmt.Errorf("sqlstore: qb import node %q: %w", name, err)
			}
			return res.LastInsertId()
		}

		total := in.TopSize
		if in.TopIsFile {
			id, err := insertNode(in.ParentID, "file", in.TopName,
				files.ChildPath(parentPath, in.TopName), blobRef(in.TopBlobID), in.TopSize, in.TopMime)
			if err != nil {
				return err
			}
			topID = id
		} else {
			var ierr error
			topID, ierr = insertNode(in.ParentID, "dir", in.TopName,
				files.ChildPath(parentPath, in.TopName), nil, 0, "")
			if ierr != nil {
				return ierr
			}
			dirs := map[string]int64{"": topID}
			dirPaths := map[string]string{"": files.ChildPath(parentPath, in.TopName)}
			for _, e := range in.Entries {
				if len(e.Segs) == 0 {
					return apperr.ErrInvalidName
				}
				rel := ""
				for _, seg := range e.Segs[:len(e.Segs)-1] {
					rel += "/" + seg
					if _, ok := dirs[rel]; ok {
						continue
					}
					parentRel := rel[:len(rel)-len(seg)-1]
					pid, ok := dirs[parentRel]
					if !ok {
						return apperr.ErrInternal.WithCause(fmt.Errorf("qb import: missing parent dir %q", parentRel))
					}
					dirPath := files.ChildPath(dirPaths[parentRel], seg)
					id, err := insertNode(pid, "dir", seg, dirPath, nil, 0, "")
					if err != nil {
						return err
					}
					dirs[rel] = id
					dirPaths[rel] = dirPath
				}
				rel = ""
				for _, seg := range e.Segs {
					rel += "/" + seg
				}
				parentRel := rel[:len(rel)-len(e.Segs[len(e.Segs)-1])-1]
				pid, ok := dirs[parentRel]
				if !ok {
					return apperr.ErrInternal.WithCause(fmt.Errorf("qb import: missing parent dir %q", parentRel))
				}
				name := e.Segs[len(e.Segs)-1]
				if _, err := insertNode(pid, "file", name,
					files.ChildPath(dirPaths[parentRel], name), blobRef(e.BlobID), e.Size, e.Mime); err != nil {
					return err
				}
				total += e.Size
			}
		}
		if total > 0 {
			res, err := tx.ExecContext(ctx,
				`UPDATE users SET used_bytes = used_bytes + ?, updated_at = ?
				 WHERE id = ? AND (quota_bytes = 0 OR used_bytes + reserved_bytes + ? <= quota_bytes)`,
				total, now, sc.UserID, total)
			if err != nil {
				return fmt.Errorf("sqlstore: qb import quota: %w", err)
			}
			if n, _ := res.RowsAffected(); n == 0 {
				return apperr.ErrQuotaExceeded.WithReason(apperr.ReasonQuotaExceeded)
			}
		}
		incRef := func(blobID int64) error {
			_, err := tx.ExecContext(ctx, `UPDATE blobs SET refcount = refcount + 1 WHERE id = ?`, blobID)
			return err
		}
		if in.TopIsFile && in.TopIncrementRef {
			if err := incRef(in.TopBlobID); err != nil {
				return fmt.Errorf("sqlstore: qb import inc ref: %w", err)
			}
		}
		for _, e := range in.Entries {
			if e.IncrementRef {
				if err := incRef(e.BlobID); err != nil {
					return fmt.Errorf("sqlstore: qb import inc ref: %w", err)
				}
			}
		}
		res, err := tx.ExecContext(ctx,
			`UPDATE transfers SET state = 'completed', error = '', node_id = ?,
			   size = ?, received_bytes = ?, finished_at = ?, updated_at = ?
			 WHERE id = ? AND state IN ('pending', 'running')`,
			topID, total, total, now, now, in.TransferID)
		if err != nil {
			return fmt.Errorf("sqlstore: qb import finish transfer: %w", err)
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return apperr.ErrInvalidState
		}
		return nil
	})
	if err != nil {
		return files.Node{}, err
	}
	return d.NodeByID(ctx, sc, topID)
}

func nullableID(id int64) any {
	if id == 0 {
		return nil
	}
	return id
}

func blobRef(id int64) *int64 {
	if id == 0 {
		return nil
	}
	return &id
}
