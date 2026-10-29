package maintenance

import (
	"context"
	"strings"
	"testing"
	"time"

	"rapig/internal/domain/access"
	"rapig/internal/infra/sql"
	storagereg "rapig/internal/infra/storage"
	"rapig/internal/infra/storage/local"
)

// secondPolicy registers another enabled root and returns its id and store.
func secondPolicy(t *testing.T, db *sqlstore.DB, registry *storagereg.Registry, name string) (int64, *local.Store) {
	t.Helper()
	ctx := context.Background()
	policy, err := db.CreateStoragePolicy(ctx, sqlstore.NewStoragePolicy{
		Name: name, RootPath: t.TempDir() + "/root", Enabled: true,
	})
	if err != nil {
		t.Fatalf("create policy: %v", err)
	}
	store, err := registry.Get(ctx, policy.ID)
	if err != nil {
		t.Fatalf("store for %s: %v", name, err)
	}
	return policy.ID, store
}

// putBlobInPolicy stores content in one root and returns its blob row.
func putBlobInPolicy(t *testing.T, db *sqlstore.DB, store *local.Store, policyID int64, content string) sqlstore.Blob {
	t.Helper()
	sha, size, err := store.Put(context.Background(), strings.NewReader(content))
	if err != nil {
		t.Fatalf("put: %v", err)
	}
	b, _, err := db.InsertBlobInPolicy(context.Background(), policyID, sha, size)
	if err != nil {
		t.Fatalf("insert blob: %v", err)
	}
	return b
}

// referenceInPolicy makes a blob reachable from a node that lives in the same
// policy (the invariant gc relies on).
func referenceInPolicy(t *testing.T, db *sqlstore.DB, userID, policyID int64, name string, b sqlstore.Blob) {
	t.Helper()
	ctx := context.Background()
	sc := access.UserScope(userID, false)
	root, err := db.EnsureRoot(ctx, userID, policyID)
	if err != nil {
		t.Fatalf("root: %v", err)
	}
	if _, err := db.CommitCompletedUpload(ctx, sc, sqlstore.CompletedUploadInput{
		ParentID:        root.ID,
		StoragePolicyID: policyID,
		Name:            name,
		Path:            name,
		Mime:            "text/plain",
		SHA256:          b.SHA256,
		Size:            b.Size,
		BlobID:          b.ID,
		IncrementRef:    false,
		CreatedAt:       time.Now(),
	}); err != nil {
		t.Fatalf("commit: %v", err)
	}
}

// TestGCCleansOnlyTheOrphanPolicyRoot is the M5.3 gc contract: identical
// content in two roots is two blob rows; the orphaned one is collected from its
// own root while the referenced copy in the other root is untouched.
func TestGCCleansOnlyTheOrphanPolicyRoot(t *testing.T) {
	ctx := context.Background()
	db, defaultStore, registry := newEnv(t)
	user := ensureUser(t, db)
	defaultPolicy, err := db.DefaultStoragePolicy(ctx)
	if err != nil {
		t.Fatalf("default policy: %v", err)
	}
	secondPolicyID, secondStore := secondPolicy(t, db, registry, "second")

	const content = "same-bytes-two-roots"
	orphan := putBlobInPolicy(t, db, defaultStore, defaultPolicy.ID, content)
	kept := putBlobInPolicy(t, db, secondStore, secondPolicyID, content)
	if orphan.SHA256 != kept.SHA256 || orphan.ID == kept.ID {
		t.Fatalf("blobs = %+v / %+v, want same sha in two rows", orphan, kept)
	}
	referenceInPolicy(t, db, user, secondPolicyID, "kept.txt", kept)

	makeOrphan(t, db, orphan, 48*time.Hour)
	if _, err := db.ExecContext(ctx,
		`UPDATE blobs SET created_at = ? WHERE id = ?`,
		time.Now().Add(-48*time.Hour).Unix(), kept.ID); err != nil {
		t.Fatalf("age kept blob: %v", err)
	}

	res, err := GC(ctx, db, registry, false, time.Now(), noLog())
	if err != nil {
		t.Fatalf("gc: %v", err)
	}
	if res.Removed != 1 || res.BytesFreed != orphan.Size {
		t.Fatalf("gc removed %d/%d bytes, want 1/%d", res.Removed, res.BytesFreed, orphan.Size)
	}
	if len(res.Policies) != 2 {
		t.Fatalf("gc reported %d policies, want 2", len(res.Policies))
	}
	for _, p := range res.Policies {
		switch p.PolicyID {
		case defaultPolicy.ID:
			if p.Removed != 1 {
				t.Fatalf("default policy removed %d, want 1", p.Removed)
			}
		case secondPolicyID:
			if p.Removed != 0 {
				t.Fatalf("second policy removed %d, want 0 (its copy is referenced)", p.Removed)
			}
		}
	}

	// The orphaned row and file are gone from the default root...
	if _, err := db.BlobByID(ctx, orphan.ID); err == nil {
		t.Fatal("orphan blob row survived gc")
	}
	if _, err := defaultStore.OpenFile(orphan.SHA256); err == nil {
		t.Fatal("orphan file survived gc")
	}
	// ...while the referenced copy in the second root is intact.
	if _, err := db.BlobByID(ctx, kept.ID); err != nil {
		t.Fatalf("referenced blob row removed: %v", err)
	}
	f, err := secondStore.OpenFile(kept.SHA256)
	if err != nil {
		t.Fatalf("referenced file removed: %v", err)
	}
	_ = f.Close()
}

// TestGCDryRunSpansEveryPolicy: a dry run counts orphans in every root and
// deletes nothing, including from disabled roots (gc must still see them).
func TestGCDryRunSpansEveryPolicy(t *testing.T) {
	ctx := context.Background()
	db, defaultStore, registry := newEnv(t)
	defaultPolicy, err := db.DefaultStoragePolicy(ctx)
	if err != nil {
		t.Fatalf("default policy: %v", err)
	}
	secondPolicyID, secondStore := secondPolicy(t, db, registry, "second")
	if _, err := db.UpdateStoragePolicy(ctx, secondPolicyID, sqlstore.StoragePolicyUpdate{Enabled: boolPtr(false)}); err != nil {
		t.Fatalf("disable: %v", err)
	}

	a := putBlobInPolicy(t, db, defaultStore, defaultPolicy.ID, "dry-a")
	b := putBlobInPolicy(t, db, secondStore, secondPolicyID, "dry-b")
	makeOrphan(t, db, a, 48*time.Hour)
	makeOrphan(t, db, b, 48*time.Hour)

	res, err := GC(ctx, db, registry, true, time.Now(), noLog())
	if err != nil {
		t.Fatalf("gc dry run: %v", err)
	}
	if res.Removed != 2 || !res.DryRun {
		t.Fatalf("dry run removed %d (dry=%v), want 2/true", res.Removed, res.DryRun)
	}
	for _, blob := range []sqlstore.Blob{a, b} {
		if _, err := db.BlobByID(ctx, blob.ID); err != nil {
			t.Fatalf("dry run deleted blob %d: %v", blob.ID, err)
		}
	}
}

// TestFsckReportsPerPolicy: fsck walks every root, reports totals that keep the
// old shape, and adds the per-policy breakdown.
func TestFsckReportsPerPolicy(t *testing.T) {
	ctx := context.Background()
	db, defaultStore, registry := newEnv(t)
	user := ensureUser(t, db)
	defaultPolicy, err := db.DefaultStoragePolicy(ctx)
	if err != nil {
		t.Fatalf("default policy: %v", err)
	}
	secondPolicyID, secondStore := secondPolicy(t, db, registry, "second")

	a := putBlobInPolicy(t, db, defaultStore, defaultPolicy.ID, "fsck-a")
	b := putBlobInPolicy(t, db, secondStore, secondPolicyID, "fsck-b")
	referenceInPolicy(t, db, user, defaultPolicy.ID, "a.txt", a)
	referenceInPolicy(t, db, user, secondPolicyID, "b.txt", b)

	rep, err := Fsck(ctx, db, registry, true, false, noLog())
	if err != nil {
		t.Fatalf("fsck: %v", err)
	}
	if rep.Issues() != 0 {
		t.Fatalf("healthy deployment reported %d issue(s): %+v", rep.Issues(), rep)
	}
	if rep.Blobs != 2 {
		t.Fatalf("fsck counted %d blobs, want 2", rep.Blobs)
	}
	if len(rep.Policies) != 2 {
		t.Fatalf("fsck reported %d policies, want 2", len(rep.Policies))
	}
	for _, p := range rep.Policies {
		if p.Blobs != 1 {
			t.Fatalf("policy %d blobs = %d, want 1", p.PolicyID, p.Blobs)
		}
		if p.RootPath == "" {
			t.Fatalf("policy %d has no root path", p.PolicyID)
		}
		if len(p.MissingFiles) != 0 {
			t.Fatalf("policy %d missing files: %v", p.PolicyID, p.MissingFiles)
		}
	}

	// Removing one file surfaces only in that policy's section.
	if err := secondStore.Remove(b.SHA256); err != nil {
		t.Fatalf("remove: %v", err)
	}
	rep, err = Fsck(ctx, db, registry, false, false, noLog())
	if err != nil {
		t.Fatalf("fsck: %v", err)
	}
	if len(rep.MissingFiles) != 1 {
		t.Fatalf("missing files = %v, want exactly the second root's file", rep.MissingFiles)
	}
	for _, p := range rep.Policies {
		found := p.PolicyID == secondPolicyID && len(p.MissingFiles) == 1
		other := p.PolicyID == defaultPolicy.ID && len(p.MissingFiles) == 0
		if !found && !other {
			t.Fatalf("policy %d missing = %v", p.PolicyID, p.MissingFiles)
		}
	}
}

func boolPtr(v bool) *bool { return &v }
