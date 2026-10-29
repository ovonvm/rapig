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

// Copy limits keep one copy request bounded (M4.1 decision).
const (
	CopyMaxItems = 10000
	CopyMaxDepth = 64
)

// copyRow is the minimal per-node state needed to duplicate a subtree.
type copyRow struct {
	ID         int64
	ParentID   int64
	Kind       files.Kind
	Name       string
	NameFolded string
	Path       string
	BlobID     sql.NullInt64
	Size       int64
	PolicyID   sql.NullInt64
	Mime       string
}

// CopyNode duplicates a subtree under a destination directory (root when
// parentRef is nil). Blob contents are shared; refcounts and used_bytes are
// updated in the same transaction. The copy root gets an automatic free name
// ("X - 副本", then " (n)") unless newName is provided.
func (d *DB) CopyNode(ctx context.Context, sc access.Scope, id int64, parentRef *int64, newName *string, now time.Time) (files.Node, error) {
	if err := sc.Validate(); err != nil {
		return files.Node{}, err
	}
	src, err := d.NodeByID(ctx, sc, id)
	if err != nil {
		return files.Node{}, err
	}
	if src.IsRoot() {
		return files.Node{}, apperr.ErrInvalidTarget
	}
	if src.State != files.StateActive {
		return files.Node{}, apperr.ErrInvalidState
	}
	target, err := d.copyTarget(ctx, sc, src, parentRef)
	if err != nil {
		return files.Node{}, err
	}
	if err := sameStoragePolicy(src, target); err != nil {
		return files.Node{}, err
	}
	if err := sameGroup(src, target); err != nil {
		return files.Node{}, err
	}

	rows, err := d.subtreeRows(ctx, src)
	if err != nil {
		return files.Node{}, err
	}
	if len(rows) > CopyMaxItems {
		return files.Node{}, apperr.ErrPayloadTooLarge.WithReason(apperr.ReasonCopyTooLarge)
	}
	var totalSize int64
	for _, r := range rows {
		if r.Kind == files.KindFile {
			totalSize += r.Size
		}
		rel := strings.TrimPrefix(r.Path, src.Path)
		if strings.Count(rel, "/") > CopyMaxDepth {
			return files.Node{}, apperr.ErrPayloadTooLarge.WithReason(apperr.ReasonCopyTooLarge)
		}
	}

	name := defaultCopyName(src.Name)
	if newName != nil {
		name = *newName
	}
	if err := files.ValidateName(name); err != nil {
		return files.Node{}, err
	}
	name = d.freeName(ctx, sc, target.ID, name, 0)
	rootPath := files.ChildPath(target.Path, name)

	ids := make(map[int64]int64, len(rows))
	var rootNewID int64
	err = d.WithTx(ctx, func(tx *sql.Tx) error {
		if totalSize > 0 {
			res, err := tx.ExecContext(ctx, `UPDATE users SET used_bytes = used_bytes + ?
				WHERE id = ? AND (quota_bytes = 0 OR used_bytes + ? <= quota_bytes)`,
				totalSize, sc.UserID, totalSize)
			if err != nil {
				return fmt.Errorf("sqlstore: copy quota: %w", err)
			}
			if n, _ := res.RowsAffected(); n == 0 {
				return apperr.ErrQuotaExceeded
			}
		}
		refs := make(map[int64]int64)
		for _, r := range rows {
			if r.BlobID.Valid {
				refs[r.BlobID.Int64]++
			}
		}
		for blobID, cnt := range refs {
			if _, err := tx.ExecContext(ctx, `UPDATE blobs SET refcount = refcount + ? WHERE id = ?`, cnt, blobID); err != nil {
				return fmt.Errorf("sqlstore: copy blob refcount: %w", err)
			}
		}
		for _, r := range rows {
			parent := ids[r.ParentID]
			if r.ID == src.ID {
				parent = target.ID
			}
			if parent == 0 {
				return fmt.Errorf("sqlstore: copy subtree: parent %d not copied", r.ParentID)
			}
			rel := strings.TrimPrefix(r.Path, src.Path)
			copyName, copyFolded := r.Name, r.NameFolded
			if r.ID == src.ID {
				copyName, copyFolded = name, files.FoldName(name)
			}
			res, err := tx.ExecContext(ctx, `INSERT INTO nodes
				(owner_id, parent_id, kind, name, name_folded, path, group_id, blob_id, size,
				 storage_policy_id, state, rev, created_at, updated_at, mime)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'active', 0, ?, ?, ?)`,
				sc.UserID, parent, string(r.Kind), copyName, copyFolded, rootPath+rel,
				src.GroupID, nullInt64(r.BlobID), r.Size, nullInt64(r.PolicyID), now.Unix(), now.Unix(), r.Mime)
			if err != nil {
				if isUniqueViolation(err) {
					return apperr.ErrNameConflict
				}
				return fmt.Errorf("sqlstore: copy node: %w", err)
			}
			nid, err := res.LastInsertId()
			if err != nil {
				return fmt.Errorf("sqlstore: copy node id: %w", err)
			}
			ids[r.ID] = nid
			if r.ID == src.ID {
				rootNewID = nid
			}
		}
		return nil
	})
	if err != nil {
		return files.Node{}, err
	}
	return d.NodeByID(ctx, sc, rootNewID)
}

// copyTarget resolves where the copy root goes and rejects copies into the
// source subtree (including the source node itself).
func (d *DB) copyTarget(ctx context.Context, sc access.Scope, src files.Node, parentRef *int64) (files.Node, error) {
	if err := sc.Validate(); err != nil {
		return files.Node{}, err
	}
	var parentID int64
	if parentRef == nil {
		if src.ParentID == nil {
			return files.Node{}, apperr.ErrParentNotFound
		}
		parentID = *src.ParentID
	} else {
		parentID = *parentRef
	}
	if parentID == 0 {
		root, err := d.RootForNode(ctx, sc, src)
		if err != nil {
			return files.Node{}, apperr.ErrParentNotFound
		}
		if root.State != files.StateActive {
			return files.Node{}, apperr.ErrParentNotFound
		}
		return root, nil
	}
	target, err := d.NodeByID(ctx, sc, parentID)
	if err != nil {
		if apperr.CodeOf(err) == apperr.CodeNotFound {
			return files.Node{}, apperr.ErrParentNotFound
		}
		return files.Node{}, err
	}
	if target.State != files.StateActive || target.Kind != files.KindDir {
		return files.Node{}, apperr.ErrParentNotFound
	}
	if target.ID == src.ID || isDescendantPath(target.Path, src.Path) {
		return files.Node{}, apperr.ErrInvalidTarget
	}
	return target, nil
}

// subtreeRows loads the active source subtree ordered parents-first. The
// boundary is the source owner's tree or, for a group node, the group tree: a
// shared folder may contain nodes uploaded by several members.
func (d *DB) subtreeRows(ctx context.Context, src files.Node) ([]copyRow, error) {
	scopeCol, scopeArg := "owner_id", any(src.OwnerID)
	if src.GroupID != nil {
		scopeCol, scopeArg = "group_id", any(*src.GroupID)
	}
	rows, err := d.QueryContext(ctx, `
		WITH RECURSIVE sub(id) AS (
			SELECT id FROM nodes WHERE id = ? AND `+scopeCol+` = ? AND state = 'active'
			UNION ALL
			SELECT n.id FROM nodes n JOIN sub s ON n.parent_id = s.id
			WHERE n.`+scopeCol+` = ? AND n.state = 'active'
		)
		SELECT n.id, n.parent_id, n.kind, n.name, n.name_folded, n.path, n.blob_id,
			n.size, n.storage_policy_id, n.mime
		FROM nodes n JOIN sub s ON n.id = s.id
		ORDER BY length(n.path) ASC, n.id ASC`, src.ID, scopeArg, scopeArg)
	if err != nil {
		return nil, fmt.Errorf("sqlstore: copy subtree: %w", err)
	}
	defer rows.Close()
	var out []copyRow
	for rows.Next() {
		var (
			r      copyRow
			kind   string
			parent sql.NullInt64
		)
		if err := rows.Scan(&r.ID, &parent, &kind, &r.Name, &r.NameFolded, &r.Path,
			&r.BlobID, &r.Size, &r.PolicyID, &r.Mime); err != nil {
			return nil, fmt.Errorf("sqlstore: scan copy row: %w", err)
		}
		r.Kind = files.Kind(kind)
		r.ParentID = parent.Int64
		out = append(out, r)
	}
	return out, rows.Err()
}

// defaultCopyName is "X - 副本", shortened when X is near the length limit.
func defaultCopyName(name string) string {
	const suffix = " - 副本"
	runes := []rune(name)
	if n := len(runes) - files.MaxNameLength + len([]rune(suffix)); n > 0 {
		runes = runes[:len(runes)-n]
	}
	return string(runes) + suffix
}

func nullInt64(v sql.NullInt64) any {
	if !v.Valid {
		return nil
	}
	return v.Int64
}
