package sqlstore

import (
	"context"
	"testing"
	"time"

	"rapig/internal/domain/access"
	"rapig/internal/domain/files"
)

func TestSearchNodes(t *testing.T) {
	db, policyID := newTestDB(t)
	ctx := context.Background()
	u := seedUser(t, db, "alice", "user")
	sc := access.UserScope(u.ID, false)
	root, err := db.EnsureRoot(ctx, u.ID, policyID)
	if err != nil {
		t.Fatalf("root: %v", err)
	}
	if _, err := db.CreateDir(ctx, sc, root.ID, "Alpha", "Alpha"); err != nil {
		t.Fatalf("create Alpha: %v", err)
	}
	if _, err := db.CreateDir(ctx, sc, root.ID, "相册2024", "相册2024"); err != nil {
		t.Fatalf("create 相册: %v", err)
	}
	mkFile(t, db, sc, root, "alpine.txt", "one")
	mkFile(t, db, sc, root, "beta.txt", "two")

	// Trigram FTS: three or more runes, case-insensitive via folded names.
	items, total, err := db.SearchNodes(ctx, sc, "alp", "", false, 50, 0)
	if err != nil {
		t.Fatalf("search alp: %v", err)
	}
	if total != 2 || len(items) != 2 {
		t.Fatalf("search alp: total=%d len=%d want 2", total, len(items))
	}

	// CJK trigram phrase match.
	items, total, err = db.SearchNodes(ctx, sc, "相册", "", false, 50, 0)
	if err != nil {
		t.Fatalf("search CJK: %v", err)
	}
	if total != 1 || len(items) != 1 || items[0].Kind != files.KindDir {
		t.Fatalf("search CJK: total=%d items=%v want one dir", total, items)
	}

	// Short queries fall back to an escaped LIKE scan.
	items, total, err = db.SearchNodes(ctx, sc, "be", "", false, 50, 0)
	if err != nil {
		t.Fatalf("search short: %v", err)
	}
	if total != 1 || items[0].Name != "beta.txt" {
		t.Fatalf("search short: total=%d items=%v want beta.txt", total, items)
	}

	// Kind filter.
	items, total, err = db.SearchNodes(ctx, sc, "al", string(files.KindDir), false, 50, 0)
	if err != nil {
		t.Fatalf("search dir kind: %v", err)
	}
	if total != 1 || len(items) != 1 || items[0].Name != "Alpha" {
		t.Fatalf("search dir kind: total=%d items=%v", total, items)
	}

	// Pagination keeps the total but pages the rows.
	items, total, err = db.SearchNodes(ctx, sc, "al", "", false, 1, 1)
	if err != nil {
		t.Fatalf("search page: %v", err)
	}
	if total != 2 || len(items) != 1 {
		t.Fatalf("search page: total=%d len=%d want total 2 len 1", total, len(items))
	}

	// Trashed nodes disappear from search.
	alpha, err := db.NodeByID(ctx, sc, mustChildID(t, db, sc, root, "Alpha"))
	if err != nil {
		t.Fatalf("load Alpha: %v", err)
	}
	if _, err := db.TrashNode(ctx, sc, alpha.ID, time.Now()); err != nil {
		t.Fatalf("trash Alpha: %v", err)
	}
	items, total, err = db.SearchNodes(ctx, sc, "Alpha", "", false, 50, 0)
	if err != nil {
		t.Fatalf("search after trash: %v", err)
	}
	if total != 0 || len(items) != 0 {
		t.Fatalf("search after trash: total=%d items=%v want empty", total, items)
	}

	// Other users never see alice's nodes.
	other := seedUser(t, db, "bob", "user")
	otherScope := access.UserScope(other.ID, false)
	items, total, err = db.SearchNodes(ctx, otherScope, "alp", "", false, 50, 0)
	if err != nil {
		t.Fatalf("search other: %v", err)
	}
	if total != 0 || len(items) != 0 {
		t.Fatalf("search other: total=%d items=%v want empty", total, items)
	}

	// Empty query is an empty result set, not an error.
	items, total, err = db.SearchNodes(ctx, sc, "   ", "", false, 50, 0)
	if err != nil || total != 0 || len(items) != 0 {
		t.Fatalf("search blank: total=%d err=%v", total, err)
	}
}

func mustChildID(t *testing.T, db *DB, sc access.Scope, parent files.Node, name string) int64 {
	t.Helper()
	items, _, err := db.ListChildren(context.Background(), sc, parent.ID, 200, 0)
	if err != nil {
		t.Fatalf("list children: %v", err)
	}
	for _, n := range items {
		if n.Name == name {
			return n.ID
		}
	}
	t.Fatalf("child %q not found", name)
	return 0
}
