package sqlstore

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"rapig/internal/domain/access"
	"rapig/internal/domain/files"
	"rapig/internal/platform/apperr"
)

// MoveNode renames and/or moves one active node. revision is the value the
// client read earlier (files.Node.Revision); a mismatch means a concurrent
// writer won and the caller must refresh (409 stale_write).
func (d *DB) MoveNode(ctx context.Context, sc access.Scope, id int64, parentRef *int64, newName *string, revision string, now time.Time) (files.Node, error) {
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
	if n.State != files.StateActive {
		return files.Node{}, apperr.ErrInvalidState
	}
	if revision == "" {
		return files.Node{}, apperr.ErrBadRequest.WithCause(fmt.Errorf("revision is required"))
	}
	rev, err := strconv.ParseInt(revision, 10, 64)
	if err != nil || rev != n.Rev {
		return files.Node{}, apperr.ErrStaleWrite
	}
	if parentRef == nil && newName == nil {
		return files.Node{}, apperr.ErrBadRequest.WithCause(fmt.Errorf("name or parent is required"))
	}

	target, err := d.moveTarget(ctx, sc, n, parentRef)
	if err != nil {
		return files.Node{}, err
	}
	if err := sameStoragePolicy(n, target); err != nil {
		return files.Node{}, err
	}
	if err := sameGroup(n, target); err != nil {
		return files.Node{}, err
	}
	name := n.Name
	if newName != nil {
		if err := files.ValidateName(*newName); err != nil {
			return files.Node{}, err
		}
		name = *newName
	}
	var conflict int
	if err := d.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM nodes
		 WHERE parent_id = ? AND state = 'active' AND name_folded = ? AND id != ?`,
		target.ID, files.FoldName(name), n.ID).Scan(&conflict); err != nil {
		return files.Node{}, fmt.Errorf("sqlstore: check move name: %w", err)
	}
	if conflict > 0 {
		return files.Node{}, apperr.ErrNameConflict
	}
	newPath := files.ChildPath(target.Path, name)

	accessSQL, accessArgs := nodeAccessSQL(sc)
	updateArgs := []any{target.ID, name, files.FoldName(name), newPath, now.Unix(), n.ID}
	updateArgs = append(updateArgs, accessArgs...)
	updateArgs = append(updateArgs, n.Rev)
	err = d.WithTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx,
			`UPDATE nodes SET parent_id = ?, name = ?, name_folded = ?, path = ?,
				updated_at = ?, rev = rev + 1
			 WHERE id = ? AND `+accessSQL+` AND state = 'active' AND rev = ?`,
			updateArgs...)
		if err != nil {
			if isUniqueViolation(err) {
				return apperr.ErrNameConflict
			}
			return fmt.Errorf("sqlstore: move node: %w", err)
		}
		if rows, _ := res.RowsAffected(); rows == 0 {
			return apperr.ErrStaleWrite
		}
		return rewriteSubtreePaths(ctx, tx, sc.UserID, n.GroupID, n.Path, newPath, n.ID)
	})
	if err != nil {
		return files.Node{}, err
	}
	return d.NodeByID(ctx, sc, n.ID)
}

// moveTarget resolves the destination directory (root when parentRef is nil).
func (d *DB) moveTarget(ctx context.Context, sc access.Scope, n files.Node, parentRef *int64) (files.Node, error) {
	if err := sc.Validate(); err != nil {
		return files.Node{}, err
	}
	if parentRef == nil {
		return d.NodeByID(ctx, sc, *n.ParentID)
	}
	if *parentRef == 0 {
		root, err := d.RootForNode(ctx, sc, n)
		if err != nil {
			return files.Node{}, apperr.ErrParentNotFound
		}
		if root.State != files.StateActive {
			return files.Node{}, apperr.ErrParentNotFound
		}
		return root, nil
	}
	target, err := d.NodeByID(ctx, sc, *parentRef)
	if err != nil {
		if apperr.CodeOf(err) == apperr.CodeNotFound {
			return files.Node{}, apperr.ErrParentNotFound
		}
		return files.Node{}, err
	}
	if target.State != files.StateActive || target.Kind != files.KindDir {
		return files.Node{}, apperr.ErrParentNotFound
	}
	if target.ID == n.ID || isDescendantPath(target.Path, n.Path) {
		return files.Node{}, apperr.ErrInvalidTarget
	}
	return target, nil
}

// sameStoragePolicy refuses a move that would cross storage roots. Blobs are
// addressed per policy root, so a node may only live where its bytes live; the
// caller gets 409 with reason cross_policy_operation (the status code is the
// existing generic conflict, only the reason is new in M5.3).
func sameStoragePolicy(node, target files.Node) error {
	nodePolicy, targetPolicy := int64(0), int64(0)
	if node.StoragePolicyID != nil {
		nodePolicy = *node.StoragePolicyID
	}
	if target.StoragePolicyID != nil {
		targetPolicy = *target.StoragePolicyID
	}
	if nodePolicy != targetPolicy {
		return apperr.ErrInvalidState.WithReason(apperr.ReasonCrossPolicyOperation).WithCause(
			fmt.Errorf("node policy %d, target policy %d", nodePolicy, targetPolicy))
	}
	return nil
}

// sameGroup refuses a move that would leave the node's space. P1 keeps personal
// trees and group trees disjoint: moving a node into another group (or into a
// personal tree) is a cross_group_operation conflict, and copy is symmetric.
func sameGroup(node, target files.Node) error {
	switch {
	case node.GroupID == nil && target.GroupID == nil:
		return nil
	case node.GroupID != nil && target.GroupID != nil && *node.GroupID == *target.GroupID:
		return nil
	default:
		return apperr.ErrCrossGroupOperation
	}
}

// isDescendantPath reports whether p is n itself or lies under n's subtree.
func isDescendantPath(p, n string) bool {
	if n == "" {
		return false
	}
	return p == n || strings.HasPrefix(p, n+"/")
}

// escapeLike neutralizes LIKE wildcards so paths with % or _ stay literal.
func escapeLike(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, "%", `\%`)
	s = strings.ReplaceAll(s, "_", `\_`)
	return s
}

// runeLen is len in characters, for SQLite substr() offsets.
func runeLen(s string) int { return utf8.RuneCountInString(s) }
