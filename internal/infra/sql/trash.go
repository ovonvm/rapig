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

const trashNodeColumns = `n.id, n.owner_id, n.parent_id, n.kind, n.name, n.name_folded, n.path,
	n.blob_id, n.size, n.storage_policy_id, n.state, n.trashed_from, n.trashed_at, n.rev,
	n.created_at, n.updated_at, n.group_id`

// trashRootWhere restricts a nodes query to trash entries shown at the top
// level: the node is trashed and its original parent is not itself trashed
// (otherwise it is just a descendant of another trash entry). Group spaces have
// no trash (P1): their deletions are permanent, so the personal trash never
// shows a grouped node.
const trashRootWhere = `n.owner_id = ? AND n.group_id IS NULL AND n.state = 'trashed'
	AND NOT EXISTS (SELECT 1 FROM nodes p WHERE p.id = n.trashed_from AND p.state = 'trashed')`

// TrashNode soft deletes a node: it keeps its place in the tree and its quota
// reservation, but disappears from normal listings. Directories are marked
// only at the root; their active children follow the parent out of the tree.
func (d *DB) TrashNode(ctx context.Context, sc access.Scope, id int64, now time.Time) (files.Node, error) {
	if err := sc.Validate(); err != nil {
		return files.Node{}, err
	}
	n, err := d.NodeByID(ctx, sc, id)
	if err != nil {
		return files.Node{}, err
	}
	if n.IsRoot() {
		return files.Node{}, apperr.ErrInvalidTarget
	}
	// 组空间没有回收站：组节点只能永久删除（PurgeGroupNode）。
	if n.GroupID != nil {
		return files.Node{}, apperr.ErrInvalidTarget
	}
	if n.State != files.StateActive {
		return files.Node{}, apperr.ErrInvalidState
	}
	res, err := d.ExecContext(ctx,
		`UPDATE nodes SET state = 'trashed', trashed_from = parent_id, trashed_at = ?,
			updated_at = ?, rev = rev + 1
		 WHERE id = ? AND owner_id = ? AND group_id IS NULL AND state = 'active'`,
		now.Unix(), now.Unix(), id, sc.UserID)
	if err != nil {
		return files.Node{}, fmt.Errorf("sqlstore: trash node: %w", err)
	}
	if rows, _ := res.RowsAffected(); rows == 0 {
		return files.Node{}, apperr.ErrInvalidState
	}
	return d.NodeByID(ctx, sc, id)
}

// ListTrash returns top-level trash entries plus the total, newest first.
func (d *DB) ListTrash(ctx context.Context, sc access.Scope, limit, offset int) ([]files.Node, int, error) {
	if err := sc.Validate(); err != nil {
		return nil, 0, err
	}
	var total int
	if err := d.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM nodes n WHERE `+trashRootWhere, sc.UserID).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("sqlstore: count trash: %w", err)
	}
	rows, err := d.QueryContext(ctx,
		`SELECT `+trashNodeColumns+`
		 FROM nodes n WHERE `+trashRootWhere+`
		 ORDER BY n.trashed_at DESC, n.id DESC LIMIT ? OFFSET ?`,
		sc.UserID, limit, offset)
	if err != nil {
		return nil, 0, fmt.Errorf("sqlstore: list trash: %w", err)
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
		return nil, 0, fmt.Errorf("sqlstore: trash rows: %w", err)
	}
	return out, total, nil
}

// RestoreNode brings a trashed node back. It prefers the original parent; when
// that parent is gone or the name is taken it falls back to the owner's root
// and appends " (n)" until the name is free. Descendant paths are rewritten.
func (d *DB) RestoreNode(ctx context.Context, sc access.Scope, id int64, now time.Time) (files.Node, error) {
	if err := sc.Validate(); err != nil {
		return files.Node{}, err
	}
	n, err := d.NodeByID(ctx, sc, id)
	if err != nil {
		return files.Node{}, err
	}
	if n.State != files.StateTrashed {
		return files.Node{}, apperr.ErrInvalidState
	}
	if n.GroupID != nil {
		return files.Node{}, apperr.ErrInvalidTarget
	}
	target, err := d.restoreTarget(ctx, sc, n)
	if err != nil {
		return files.Node{}, err
	}
	name := n.Name
	if n.Kind == files.KindDir || !n.IsRoot() {
		name = d.freeName(ctx, sc, target.ID, n.Name, id)
	}
	newPath := files.ChildPath(target.Path, name)

	if err := d.WithTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx,
			`UPDATE nodes SET parent_id = ?, name = ?, name_folded = ?, path = ?,
				state = 'active', trashed_from = NULL, trashed_at = NULL,
				updated_at = ?, rev = rev + 1
			 WHERE id = ? AND owner_id = ? AND state = 'trashed'`,
			target.ID, name, files.FoldName(name), newPath, now.Unix(), id, sc.UserID)
		if err != nil {
			return fmt.Errorf("sqlstore: restore node: %w", err)
		}
		if rows, _ := res.RowsAffected(); rows == 0 {
			return apperr.ErrInvalidState
		}
		return rewriteSubtreePaths(ctx, tx, sc.UserID, nil, n.Path, newPath, id)
	}); err != nil {
		return files.Node{}, err
	}
	return d.NodeByID(ctx, sc, id)
}

// restoreTarget picks where a trashed node goes back to.
func (d *DB) restoreTarget(ctx context.Context, sc access.Scope, n files.Node) (files.Node, error) {
	if err := sc.Validate(); err != nil {
		return files.Node{}, err
	}
	if n.TrashedFrom != nil {
		if parent, err := d.NodeByID(ctx, sc, *n.TrashedFrom); err == nil && parent.State == files.StateActive && parent.Kind == files.KindDir {
			return parent, nil
		}
	}
	root, err := d.RootNode(ctx, sc)
	if err != nil {
		if apperr.CodeOf(err) != apperr.CodeNotFound {
			return files.Node{}, err
		}
		policyID, perr := d.defaultPolicyID(ctx)
		if perr != nil {
			return files.Node{}, perr
		}
		return d.EnsureRoot(ctx, sc.UserID, policyID)
	}
	if root.State != files.StateActive {
		return files.Node{}, apperr.ErrParentNotFound
	}
	return root, nil
}

// freeName returns name unchanged when it is free in parentID, otherwise the
// first free "name (n)" variant. Both the folded name and the real name are
// checked because uniqueness uses the folded form. Sibling uniqueness is per
// parent, and a parent id is globally unique, so the group dimension needs no
// extra condition (a group root and a personal root never share an id).
func (d *DB) freeName(ctx context.Context, sc access.Scope, parentID int64, name string, exceptID int64) string {
	if err := sc.Validate(); err != nil {
		return name
	}
	candidates := []string{name}
	base := strings.TrimSuffix(name, " ")
	for i := 1; i <= 99; i++ {
		candidates = append(candidates, fmt.Sprintf("%s (%d)", base, i))
	}
	for _, cand := range candidates {
		var exists int
		err := d.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM nodes
			 WHERE parent_id = ? AND state = 'active' AND name_folded = ? AND id != ?`,
			parentID, files.FoldName(cand), exceptID).Scan(&exists)
		if err == nil && exists == 0 {
			return cand
		}
	}
	return fmt.Sprintf("%s (%d)", base, time.Now().UnixNano()%100000)
}

// defaultPolicyID returns the first storage policy id, creating nothing.
func (d *DB) defaultPolicyID(ctx context.Context) (int64, error) {
	var id int64
	if err := d.QueryRowContext(ctx, `SELECT id FROM storage_policies ORDER BY id LIMIT 1`).Scan(&id); err != nil {
		return 0, fmt.Errorf("sqlstore: select storage policy: %w", err)
	}
	return id, nil
}

// rewriteSubtreePaths replaces the old path prefix with the new one for every
// descendant of nodeID (the node itself is already updated by the caller).
// groupID != nil scopes the rewrite to one group; otherwise it is the owner's
// personal tree.
func rewriteSubtreePaths(ctx context.Context, tx *sql.Tx, ownerID int64, groupID *int64, oldPath, newPath string, nodeID int64) error {
	if oldPath == "" {
		return nil
	}
	scopeCol, scopeArg := "owner_id", any(ownerID)
	if groupID != nil {
		scopeCol, scopeArg = "group_id", any(*groupID)
	}
	res, err := tx.ExecContext(ctx,
		`UPDATE nodes SET path = ? || substr(path, ?)
		 WHERE `+scopeCol+` = ? AND id != ? AND path LIKE ? ESCAPE '\'`,
		newPath, runeLen(oldPath)+1, scopeArg, nodeID, escapeLike(oldPath)+"/%")
	if err != nil {
		return fmt.Errorf("sqlstore: rewrite subtree paths: %w", err)
	}
	_ = res
	return nil
}

// OrphanObject identifies an object file whose last blob reference disappeared.
// Since M5.3 a blob belongs to one storage policy, so the policy id travels with
// the hash: the caller deletes the file from that policy's root.
type OrphanObject struct {
	StoragePolicyID int64
	SHA256          string
}

// PurgeNode permanently deletes a personal node and its subtree, releases its
// quota and decrements blob references. It returns the objects whose last
// reference disappeared, so the caller can delete the files on disk.
func (d *DB) PurgeNode(ctx context.Context, sc access.Scope, id int64, now time.Time) ([]OrphanObject, error) {
	if err := sc.Validate(); err != nil {
		return nil, err
	}
	n, err := d.NodeByID(ctx, sc, id)
	if err != nil {
		return nil, err
	}
	if n.IsRoot() {
		return nil, apperr.ErrInvalidTarget
	}
	if n.GroupID != nil {
		// 组节点走 PurgeGroupNode：两个空间共用一张表，但删除语义不同。
		return nil, apperr.ErrInvalidTarget
	}
	var orphans []OrphanObject
	err = d.WithTx(ctx, func(tx *sql.Tx) error {
		var err error
		orphans, err = purgeSubtreeTx(ctx, tx, n, now)
		return err
	})
	return orphans, err
}

// PurgeGroupNode permanently deletes a node inside a group space. Group spaces
// have no trash (P1 §3): this is the only delete path for them, and it removes
// the whole subtree in one transaction, releasing each uploader's quota and
// decrementing blob refcounts.
//
// The caller (transport) has already checked the delete permission on the
// node's group; the scope still bounds the row so a foreign id is a 404.
func (d *DB) PurgeGroupNode(ctx context.Context, sc access.Scope, id int64, now time.Time) ([]OrphanObject, error) {
	if err := sc.Validate(); err != nil {
		return nil, err
	}
	n, err := d.NodeByID(ctx, sc, id)
	if err != nil {
		return nil, err
	}
	if n.IsRoot() {
		// 组根只能通过删除组消失（组必须为空），不能当普通节点删。
		return nil, apperr.ErrInvalidTarget
	}
	if n.GroupID == nil {
		return nil, apperr.ErrInvalidTarget
	}
	var orphans []OrphanObject
	err = d.WithTx(ctx, func(tx *sql.Tx) error {
		var err error
		orphans, err = purgeSubtreeTx(ctx, tx, n, now)
		return err
	})
	return orphans, err
}

// EmptyTrash purges every trashed subtree of the scope and returns how many
// top-level entries were removed plus the now unused object hashes.
func (d *DB) EmptyTrash(ctx context.Context, sc access.Scope, now time.Time) (int, []OrphanObject, error) {
	if err := sc.Validate(); err != nil {
		return 0, nil, err
	}
	rows, err := d.QueryContext(ctx,
		`SELECT `+trashNodeColumns+`
		 FROM nodes n WHERE `+trashRootWhere+`
		 ORDER BY n.trashed_at DESC`, sc.UserID)
	if err != nil {
		return 0, nil, fmt.Errorf("sqlstore: select trash roots: %w", err)
	}
	var roots []files.Node
	for rows.Next() {
		n, err := scanNode(rows)
		if err != nil {
			rows.Close()
			return 0, nil, err
		}
		roots = append(roots, n)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, nil, fmt.Errorf("sqlstore: trash rows: %w", err)
	}
	rows.Close()
	if len(roots) == 0 {
		return 0, nil, nil
	}
	var orphans []OrphanObject
	err = d.WithTx(ctx, func(tx *sql.Tx) error {
		for _, r := range roots {
			shas, err := purgeSubtreeTx(ctx, tx, r, now)
			if err != nil {
				return err
			}
			orphans = append(orphans, shas...)
		}
		return nil
	})
	if err != nil {
		return 0, nil, err
	}
	return len(roots), orphans, nil
}

// PurgeExpiredTrash removes trash entries older than cutoff across all owners.
// It is maintenance code, so it does not take a caller scope; each purge still
// stays within one owner's rows.
func (d *DB) PurgeExpiredTrash(ctx context.Context, cutoff time.Time, limit int) (int, []OrphanObject, error) {
	rows, err := d.QueryContext(ctx,
		`SELECT `+trashNodeColumns+`
		 FROM nodes n WHERE n.state = 'trashed' AND n.trashed_at IS NOT NULL AND n.trashed_at < ?
			AND NOT EXISTS (SELECT 1 FROM nodes p WHERE p.id = n.trashed_from AND p.state = 'trashed')
		 ORDER BY n.trashed_at ASC LIMIT ?`, cutoff.Unix(), limit)
	if err != nil {
		return 0, nil, fmt.Errorf("sqlstore: select expired trash: %w", err)
	}
	var roots []files.Node
	for rows.Next() {
		n, err := scanNode(rows)
		if err != nil {
			rows.Close()
			return 0, nil, err
		}
		roots = append(roots, n)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, nil, fmt.Errorf("sqlstore: expired trash rows: %w", err)
	}
	rows.Close()
	if len(roots) == 0 {
		return 0, nil, nil
	}
	var orphans []OrphanObject
	now := time.Now()
	err = d.WithTx(ctx, func(tx *sql.Tx) error {
		for _, r := range roots {
			shas, err := purgeSubtreeTx(ctx, tx, r, now)
			if err != nil {
				return err
			}
			orphans = append(orphans, shas...)
		}
		return nil
	})
	if err != nil {
		return 0, nil, err
	}
	return len(roots), orphans, nil
}

// purgeSubtreeTx deletes one subtree inside tx and returns orphaned object
// hashes. The boundary is the root's owner (personal tree) or its group (shared
// tree), so a group purge never touches another member's personal rows.
//
// Quota is released per owner: every file node was charged to whoever uploaded
// it (P1 §7), so a shared folder can contain files from several accounts.
func purgeSubtreeTx(ctx context.Context, tx *sql.Tx, root files.Node, now time.Time) ([]OrphanObject, error) {
	// Root path is empty only for a root node, which never gets purged.
	if root.Path == "" {
		return nil, apperr.ErrInvalidTarget
	}
	scopeCol, scopeArg := "owner_id", any(root.OwnerID)
	if root.GroupID != nil {
		scopeCol, scopeArg = "group_id", any(*root.GroupID)
	}
	like := escapeLike(root.Path) + "/%"
	rows, err := tx.QueryContext(ctx,
		`SELECT owner_id, blob_id, size FROM nodes
		 WHERE `+scopeCol+` = ? AND state != 'purged' AND kind = 'file' AND blob_id IS NOT NULL
		   AND (id = ? OR path LIKE ? ESCAPE '\')`, scopeArg, root.ID, like)
	if err != nil {
		return nil, fmt.Errorf("sqlstore: gather purge blobs: %w", err)
	}
	type ref struct {
		owner int64
		id    int64
		size  int64
	}
	var refs []ref
	used := make(map[int64]int64)
	for rows.Next() {
		var r ref
		if err := rows.Scan(&r.owner, &r.id, &r.size); err != nil {
			rows.Close()
			return nil, fmt.Errorf("sqlstore: scan purge blob: %w", err)
		}
		refs = append(refs, r)
		used[r.owner] += r.size
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, fmt.Errorf("sqlstore: purge blob rows: %w", err)
	}
	rows.Close()

	if _, err := tx.ExecContext(ctx,
		`DELETE FROM nodes WHERE `+scopeCol+` = ? AND (id = ? OR path LIKE ? ESCAPE '\')`,
		scopeArg, root.ID, like); err != nil {
		return nil, fmt.Errorf("sqlstore: delete subtree: %w", err)
	}
	for owner, size := range used {
		if size <= 0 {
			continue
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE users SET used_bytes = CASE WHEN used_bytes >= ? THEN used_bytes - ? ELSE 0 END
			 WHERE id = ?`, size, size, owner); err != nil {
			return nil, fmt.Errorf("sqlstore: release purge quota: %w", err)
		}
	}
	var orphans []OrphanObject
	for _, r := range refs {
		var remaining int
		if err := tx.QueryRowContext(ctx,
			`SELECT refcount FROM blobs WHERE id = ?`, r.id).Scan(&remaining); err != nil {
			if err == sql.ErrNoRows {
				continue
			}
			return nil, fmt.Errorf("sqlstore: select blob refcount: %w", err)
		}
		if remaining > 1 {
			if _, err := tx.ExecContext(ctx,
				`UPDATE blobs SET refcount = refcount - 1 WHERE id = ?`, r.id); err != nil {
				return nil, fmt.Errorf("sqlstore: decrement blob: %w", err)
			}
			continue
		}
		var sha string
		var policyID int64
		if err := tx.QueryRowContext(ctx, `SELECT sha256, storage_policy_id FROM blobs WHERE id = ?`, r.id).
			Scan(&sha, &policyID); err != nil {
			if err == sql.ErrNoRows {
				continue
			}
			return nil, fmt.Errorf("sqlstore: select blob sha: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM blobs WHERE id = ?`, r.id); err != nil {
			return nil, fmt.Errorf("sqlstore: delete blob row: %w", err)
		}
		orphans = append(orphans, OrphanObject{StoragePolicyID: policyID, SHA256: sha})
	}
	return orphans, nil
}
