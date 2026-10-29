package sqlstore

import (
	"context"
	"fmt"

	"rapig/internal/domain/access"
	"rapig/internal/domain/files"
)

// RootNode loads the owner's personal root node inside scope. Group roots are
// not returned here: they are reached through their group id (see groups.go).
func (d *DB) RootNode(ctx context.Context, sc access.Scope) (files.Node, error) {
	if err := sc.Validate(); err != nil {
		return files.Node{}, err
	}
	q := `SELECT ` + nodeColumns + ` FROM nodes
		WHERE owner_id = ? AND parent_id IS NULL AND group_id IS NULL`
	n, err := scanNode(d.QueryRowContext(ctx, q, sc.UserID))
	if err != nil {
		return files.Node{}, err
	}
	return n, nil
}

// RootForNode returns the root of the space a node lives in: the owner's
// personal root, or the group root when the node is part of a group. The API
// uses parent id 0 as "the root", which must mean the root of the node's own
// space, not always the personal one.
func (d *DB) RootForNode(ctx context.Context, sc access.Scope, n files.Node) (files.Node, error) {
	if err := sc.Validate(); err != nil {
		return files.Node{}, err
	}
	if n.GroupID == nil {
		return d.RootNode(ctx, sc)
	}
	return d.GroupRootNode(ctx, sc, *n.GroupID)
}

// GroupRootNode loads the root of a group space. It is only reachable for
// members of that group: the access predicate is applied just like on any other
// node read.
func (d *DB) GroupRootNode(ctx context.Context, sc access.Scope, groupID int64) (files.Node, error) {
	if err := sc.Validate(); err != nil {
		return files.Node{}, err
	}
	accessSQL, accessArgs := nodeAccessSQL(sc)
	q := `SELECT ` + nodeColumns + ` FROM nodes
		WHERE group_id = ? AND parent_id IS NULL AND ` + accessSQL
	args := append([]any{groupID}, accessArgs...)
	return scanNode(d.QueryRowContext(ctx, q, args...))
}

// CountNodes is a test/diagnostic helper.
func (d *DB) CountNodes(ctx context.Context) (int, error) {
	var n int
	if err := d.QueryRowContext(ctx, `SELECT COUNT(*) FROM nodes`).Scan(&n); err != nil {
		return 0, fmt.Errorf("sqlstore: count nodes: %w", err)
	}
	return n, nil
}
