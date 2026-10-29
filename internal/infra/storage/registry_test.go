package storage

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"rapig/internal/infra/sql"
	"rapig/migrations"
)

func newRegistry(t *testing.T) (*Registry, *sqlstore.DB, int64) {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	db, err := sqlstore.Open(ctx, filepath.Join(dir, "rapig.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Migrate(ctx, migrations.FS); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	defaultID, err := db.EnsureDefaultStoragePolicy(ctx, filepath.Join(dir, "storage"))
	if err != nil {
		t.Fatalf("ensure default: %v", err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	return NewRegistry(db, log), db, defaultID
}

// TestRegistryCachesStoresAndInvalidates covers the lazy creation contract:
// Get probes a root once, reuses the store, and forgets it after Invalidate
// (the admin API calls that after moving a root).
func TestRegistryCachesStoresAndInvalidates(t *testing.T) {
	ctx := context.Background()
	reg, _, defaultID := newRegistry(t)

	first, err := reg.Get(ctx, defaultID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if first.Root() == "" {
		t.Fatal("store has empty root")
	}
	again, err := reg.Get(ctx, defaultID)
	if err != nil {
		t.Fatalf("get again: %v", err)
	}
	if again != first {
		t.Fatal("registry created a second store instead of reusing the cached one")
	}

	reg.Invalidate(defaultID)
	after, err := reg.Get(ctx, defaultID)
	if err != nil {
		t.Fatalf("get after invalidate: %v", err)
	}
	if after == first {
		t.Fatal("Invalidate kept the cached store")
	}
	// Same root, so the store still points at the same directory.
	if after.Root() != first.Root() {
		t.Fatalf("root = %q, want %q", after.Root(), first.Root())
	}
}

// TestRegistryDefaultAndAll checks the default lookup and the sweep list the
// gc/fsck commands use (every policy, disabled included).
func TestRegistryDefaultAndAll(t *testing.T) {
	ctx := context.Background()
	reg, db, defaultID := newRegistry(t)

	id, store, err := reg.Default(ctx)
	if err != nil {
		t.Fatalf("default: %v", err)
	}
	if id != defaultID || store == nil {
		t.Fatalf("default = %d/%v, want %d/non-nil", id, store, defaultID)
	}

	secondRoot := filepath.Join(t.TempDir(), "second")
	second, err := db.CreateStoragePolicy(ctx, sqlstore.NewStoragePolicy{
		Name: "second", RootPath: secondRoot, Enabled: false,
	})
	if err != nil {
		t.Fatalf("create policy: %v", err)
	}

	all, err := reg.All(ctx)
	if err != nil {
		t.Fatalf("all: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("all len = %d, want 2", len(all))
	}
	seen := map[int64]bool{}
	for _, p := range all {
		seen[p.ID] = true
		if p.ID == second.ID && p.Enabled {
			t.Fatal("disabled policy reported as enabled")
		}
	}
	if !seen[defaultID] || !seen[second.ID] {
		t.Fatalf("all = %+v, missing a policy", all)
	}

	// A disabled policy still resolves a store for the read paths, and
	// Writable refuses it.
	if _, err := reg.Get(ctx, second.ID); err != nil {
		t.Fatalf("get disabled: %v", err)
	}
	if _, err := reg.Writable(ctx, second.ID); !errors.Is(err, ErrPolicyDisabled) {
		t.Fatalf("writable disabled err = %v, want ErrPolicyDisabled", err)
	}
}

// TestRegistryUnknownPolicy pins the sentinel the services translate into an
// internal error (missing policy) or a bad_request (admin API).
func TestRegistryUnknownPolicy(t *testing.T) {
	ctx := context.Background()
	reg, _, _ := newRegistry(t)

	if _, err := reg.Get(ctx, 987654); !errors.Is(err, ErrPolicyNotFound) {
		t.Fatalf("get unknown err = %v, want ErrPolicyNotFound", err)
	}
	if _, _, err := reg.Default(ctx); err != nil {
		// A fresh database always has a default; this keeps the contract
		// honest if that ever changes.
		t.Fatalf("default: %v", err)
	}
}

// TestRegistryProbeEnabledFailsWhenRootUnwritable checks the startup probe:
// a configured root that cannot be created or written is reported, not
// silently ignored.
func TestRegistryProbeEnabledFailsWhenRootUnwritable(t *testing.T) {
	ctx := context.Background()
	reg, db, _ := newRegistry(t)

	// A root pointing at an existing regular file cannot be created as a
	// directory, so the probe must fail.
	dir := t.TempDir()
	file := filepath.Join(dir, "not-a-dir")
	if err := writeFile(file); err != nil {
		t.Fatalf("write file: %v", err)
	}
	if _, err := db.CreateStoragePolicy(ctx, sqlstore.NewStoragePolicy{
		Name: "broken", RootPath: filepath.Join(file, "sub"), Enabled: true,
	}); err != nil {
		t.Fatalf("create policy: %v", err)
	}
	if err := reg.ProbeEnabled(ctx); err == nil {
		t.Fatal("ProbeEnabled succeeded for an unwritable root")
	}
}

func writeFile(path string) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	return f.Close()
}
