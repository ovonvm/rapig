package sqlstore

import (
	"context"
	"strings"
	"unicode/utf8"

	"rapig/internal/domain/access"
	"rapig/internal/domain/files"
)

// searchColumns qualifies node columns because the FTS join also exposes a
// "name" column; keep the order in sync with scanNode in nodes.go.
const searchColumns = `n.id, n.owner_id, n.parent_id, n.kind, n.name, n.name_folded,
	n.path, n.blob_id, n.size, n.storage_policy_id, n.state, n.trashed_from,
	n.trashed_at, n.rev, n.created_at, n.updated_at, n.group_id`

// SearchNodes searches the caller's personal active nodes by name. Group nodes
// are excluded (group_id IS NULL): 个人全局搜索只覆盖个人空间，组内搜索走
// SearchGroupNodes，避免把共享内容混进个人结果。
//
// kind is "" (all), files.KindFile or files.KindDir. recent orders by
// modification time instead of match quality. Queries of two runes or fewer use
// an escaped LIKE scan (trigram FTS needs at least three runes); longer queries
// use FTS5.
func (d *DB) SearchNodes(ctx context.Context, sc access.Scope, query, kind string, recent bool, limit, offset int) ([]files.Node, int, error) {
	if err := sc.Validate(); err != nil {
		return nil, 0, err
	}
	base := `n.group_id IS NULL AND n.owner_id = ?`
	return d.searchScoped(ctx, sc, base, []any{sc.UserID}, query, kind, recent, limit, offset)
}

// SearchGroupNodes searches active nodes of one group. The caller must have
// already checked read permission on the group; the access fragment additionally
// requires the group to be inside the scope so a bug cannot widen the search.
func (d *DB) SearchGroupNodes(ctx context.Context, sc access.Scope, groupID int64, query, kind string, recent bool, limit, offset int) ([]files.Node, int, error) {
	if err := sc.Validate(); err != nil {
		return nil, 0, err
	}
	accessSQL, accessArgs := nodeAccessSQLFor(sc, "n.")
	base := "n.group_id = ? AND " + accessSQL
	args := append([]any{groupID}, accessArgs...)
	return d.searchScoped(ctx, sc, base, args, query, kind, recent, limit, offset)
}

// searchScoped is the shared query builder. base is a WHERE fragment with its
// bind arguments in SQL order; the caller has already validated its scope.
func (d *DB) searchScoped(ctx context.Context, sc access.Scope, base string, baseArgs []any, query, kind string, recent bool, limit, offset int) ([]files.Node, int, error) {
	if err := sc.Validate(); err != nil {
		return nil, 0, err
	}
	folded := files.FoldName(query)
	if folded == "" {
		return []files.Node{}, 0, nil
	}
	kindCond := ""
	kindArgs := []any{}
	if kind != "" {
		kindCond = " AND n.kind = ?"
		kindArgs = append(kindArgs, kind)
	}
	where := base + kindCond + ` AND n.state = 'active'`
	args := append(append([]any{}, baseArgs...), kindArgs...)
	from := ` FROM nodes n`
	if utf8.RuneCountInString(folded) >= 3 {
		where += " AND nodes_fts MATCH ?"
		from = ` FROM nodes_fts JOIN nodes n ON n.id = nodes_fts.rowid`
		args = append(args, ftsQuery(folded))
	} else {
		where += ` AND n.name_folded LIKE ? ESCAPE '\'`
		args = append(args, "%"+escapeLike(folded)+"%")
	}
	return d.searchNodes(ctx, sc, from, where, args, folded, recent, limit, offset)
}

// searchNodes runs the count and page query. sc is only used for validation.
func (d *DB) searchNodes(ctx context.Context, sc access.Scope, from, where string, args []any, folded string, recent bool, limit, offset int) ([]files.Node, int, error) {
	if err := sc.Validate(); err != nil {
		return nil, 0, err
	}
	var total int
	if err := d.QueryRowContext(ctx, `SELECT COUNT(*)`+from+` WHERE `+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	order := ` ORDER BY n.updated_at DESC, n.id DESC`
	qargs := append(append([]any{}, args...), limit, offset)
	if !recent {
		order = ` ORDER BY CASE WHEN n.name_folded LIKE ? ESCAPE '\' THEN 0 ELSE 1 END,
			length(n.name_folded), n.updated_at DESC, n.id DESC`
		qargs = append(append([]any{}, args...), escapeLike(folded)+"%", limit, offset)
	}
	rows, err := d.QueryContext(ctx, `SELECT `+searchColumns+from+` WHERE `+where+order+` LIMIT ? OFFSET ?`, qargs...)
	if err != nil {
		return nil, 0, err
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
	return out, total, rows.Err()
}

// ftsQuery wraps the folded query as one FTS5 phrase, doubling embedded
// quotes. Trigram phrase matching gives substring semantics.
func ftsQuery(folded string) string {
	return `"` + strings.ReplaceAll(folded, `"`, `""`) + `"`
}
