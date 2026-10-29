package sqlstore

import (
	"context"
	"database/sql"
	"fmt"

	"rapig/internal/domain/access"
	"rapig/internal/domain/files"
	"rapig/internal/platform/apperr"
)

// FileContent loads a file node plus the content metadata needed to serve a
// download. Directories and missing files map to apperr.ErrNotFound; a node
// without a blob (corrupted row) is reported as ErrNotFound too so the
// handler can decide to log fs_missing_blob.
func (d *DB) FileContent(ctx context.Context, sc access.Scope, id int64) (files.Node, string, string, error) {
	if err := sc.Validate(); err != nil {
		return files.Node{}, "", "", err
	}
	accessSQL, accessArgs := nodeAccessSQLFor(sc, "n.")
	q := `SELECT n.id, n.owner_id, n.parent_id, n.kind, n.name, n.name_folded, n.path,
		n.group_id, n.blob_id, n.size, n.storage_policy_id, n.state, n.mime, n.created_at, n.updated_at,
		b.sha256
		FROM nodes n LEFT JOIN blobs b ON b.id = n.blob_id
		WHERE n.id = ? AND ` + accessSQL + ` AND n.state = 'active' AND n.kind = 'file'`
	var (
		n        files.Node
		parent   sql.NullInt64
		group    sql.NullInt64
		blob     sql.NullInt64
		policy   sql.NullInt64
		mime     string
		sha      sql.NullString
		created  int64
		updated  int64
		kindText string
	)
	args := append([]any{id}, accessArgs...)
	err := d.QueryRowContext(ctx, q, args...).Scan(
		&n.ID, &n.OwnerID, &parent, &kindText, &n.Name, &n.NameFolded, &n.Path,
		&group, &blob, &n.Size, &policy, &n.State, &mime, &created, &updated, &sha)
	if err == sql.ErrNoRows {
		return files.Node{}, "", "", apperr.ErrNotFound
	}
	if err != nil {
		return files.Node{}, "", "", fmt.Errorf("sqlstore: file content: %w", err)
	}
	n.Kind = files.Kind(kindText)
	if parent.Valid {
		n.ParentID = &parent.Int64
	}
	if group.Valid {
		n.GroupID = &group.Int64
	}
	if blob.Valid {
		n.BlobID = &blob.Int64
	}
	if policy.Valid {
		n.StoragePolicyID = &policy.Int64
	}
	n.CreatedAt = unixUTC(created)
	n.UpdatedAt = unixUTC(updated)
	if !sha.Valid || sha.String == "" {
		// Node exists but points at no object: the content simply is not
		// there. The download handler logs this as fs_missing_blob.
		return n, "", mime, apperr.ErrNotFound.WithReason(apperr.ReasonMissingAsset)
	}
	return n, sha.String, mime, nil
}
