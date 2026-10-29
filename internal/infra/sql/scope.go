package sqlstore

import (
	"strings"

	"rapig/internal/domain/access"
)

// nodeAccessSQL returns the WHERE fragment that restricts a nodes query to the
// caller's visible tree: personal nodes owned by the scope user plus every node
// that belongs to one of the scope groups.
//
// The fragment is always parenthesised and always starts with the owner branch,
// so callers can append " AND ..." safely. A scope without groups yields only
// the owner branch, which is exactly the pre-P1 query.
//
// The first statement is a Scope.Validate() guard (see guard_test.go). An
// invalid scope fails closed: the fragment matches no row at all.
func nodeAccessSQL(sc access.Scope) (string, []any) {
	if err := sc.Validate(); err != nil {
		return "0 = 1", nil
	}
	return nodeAccessSQLFor(sc, "")
}

// nodeAccessSQLFor is nodeAccessSQL with a column qualifier ("n." for joins).
func nodeAccessSQLFor(sc access.Scope, prefix string) (string, []any) {
	if err := sc.Validate(); err != nil {
		return "0 = 1", nil
	}
	col := func(name string) string {
		if prefix == "" {
			return name
		}
		return prefix + name
	}
	clause := "(" + col("group_id") + " IS NULL AND " + col("owner_id") + " = ?"
	args := []any{sc.UserID}
	if len(sc.GroupIDs) > 0 {
		clause += " OR " + col("group_id") + " IN (" + placeholders(len(sc.GroupIDs)) + ")"
		for _, id := range sc.GroupIDs {
			args = append(args, id)
		}
	}
	return clause + ")", args
}

// groupNodeSQL restricts a query to one group's nodes regardless of owner. It
// is used by the group service, which authorizes the caller itself (there is no
// per-row scope: the group is the boundary).
func groupNodeSQL(groupID int64) (string, []any) {
	return "group_id = ?", []any{groupID}
}

// placeholders returns n comma separated "?" markers.
func placeholders(n int) string {
	if n <= 0 {
		return ""
	}
	return strings.TrimSuffix(strings.Repeat("?, ", n), ", ")
}
