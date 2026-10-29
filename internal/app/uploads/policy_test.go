package uploads

import (
	"context"
	"strings"
	"testing"

	"rapig/internal/domain/access"
	domainfiles "rapig/internal/domain/files"
	"rapig/internal/infra/sql"
	"rapig/internal/platform/apperr"
)

// secondRoot registers another enabled local policy and returns a directory in
// it, created inside the caller's root so both trees are reachable from one
// principal.
func (e *env) secondRoot(t *testing.T, name string, enabled bool) (sqlstore.StoragePolicy, domainfiles.Node) {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	policy, err := e.db.CreateStoragePolicy(ctx, sqlstore.NewStoragePolicy{
		Name: name, RootPath: dir + "/root", Enabled: enabled,
	})
	if err != nil {
		t.Fatalf("create policy %s: %v", name, err)
	}
	parent := e.root(t)
	node, err := e.files.CreateDir(ctx, e.principal(), parent, name, &policy.ID)
	if err != nil {
		t.Fatalf("create dir in %s: %v", name, err)
	}
	if node.StoragePolicyID == nil || *node.StoragePolicyID != policy.ID {
		t.Fatalf("dir policy = %v, want %d", node.StoragePolicyID, policy.ID)
	}
	return policy, node
}

func (e *env) upload(t *testing.T, parent domainfiles.Node, name, body string) domainfiles.Node {
	t.Helper()
	ctx := context.Background()
	p := e.principal()
	res, err := e.svc.Init(ctx, p, parent, InitRequest{Name: name, Size: int64(len(body))})
	if err != nil {
		t.Fatalf("init %s: %v", name, err)
	}
	if err := e.svc.PutPart(ctx, p, res.UploadID, 0, strings.NewReader(body)); err != nil {
		t.Fatalf("put part %s: %v", name, err)
	}
	node, err := e.svc.Complete(ctx, p, res.UploadID, map[int64]string{0: sha256Hex([]byte(body))})
	if err != nil {
		t.Fatalf("complete %s: %v", name, err)
	}
	return node
}

// TestUploadSameContentInTwoPolicies pins the per-policy dedup key: identical
// bytes in two roots are two blobs (one per root), and each node reads its own
// copy through the policy store.
func TestUploadSameContentInTwoPolicies(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	root := e.root(t)
	second, secondDir := e.secondRoot(t, "second", true)

	const body = "shared-content"
	inDefault := e.upload(t, root, "default.txt", body)
	inSecond := e.upload(t, secondDir, "second.txt", body)

	if inDefault.StoragePolicyID == nil || *inDefault.StoragePolicyID != e.policy {
		t.Fatalf("default node policy = %v, want %d", inDefault.StoragePolicyID, e.policy)
	}
	if inSecond.StoragePolicyID == nil || *inSecond.StoragePolicyID != second.ID {
		t.Fatalf("second node policy = %v, want %d", inSecond.StoragePolicyID, second.ID)
	}
	if inDefault.BlobID == nil || inSecond.BlobID == nil {
		t.Fatal("nodes have no blob")
	}
	if *inDefault.BlobID == *inSecond.BlobID {
		t.Fatal("both roots share one blob row; the key must be (policy, sha256)")
	}

	sha := sha256Hex([]byte(body))
	blobDefault, err := e.db.BlobBySHAInPolicy(ctx, e.policy, sha)
	if err != nil {
		t.Fatalf("blob in default policy: %v", err)
	}
	blobSecond, err := e.db.BlobBySHAInPolicy(ctx, second.ID, sha)
	if err != nil {
		t.Fatalf("blob in second policy: %v", err)
	}
	if blobDefault.ID == blobSecond.ID {
		t.Fatal("BlobBySHAInPolicy returned the same row for two policies")
	}
	if _, err := e.db.BlobBySHAInPolicy(ctx, e.policy, strings.Repeat("0", 64)); apperr.CodeOf(err) != apperr.CodeNotFound {
		t.Fatalf("unknown sha err = %v, want not found", err)
	}

	// Both files read back the right bytes from their own root.
	for _, n := range []domainfiles.Node{inDefault, inSecond} {
		_, shaOut, _, store, err := e.files.Content(ctx, e.principal(), n.ID)
		if err != nil {
			t.Fatalf("content %s: %v", n.Name, err)
		}
		f, err := store.OpenFile(shaOut)
		if err != nil {
			t.Fatalf("open %s: %v", n.Name, err)
		}
		data := make([]byte, 64)
		nr, _ := f.Read(data)
		_ = f.Close()
		if string(data[:nr]) != body {
			t.Fatalf("%s content = %q, want %q", n.Name, data[:nr], body)
		}
	}

	// A second upload of the same bytes inside the second policy dedupes to
	// that policy's existing blob and does not create a third row.
	dedup := e.upload(t, secondDir, "second-copy.txt", body)
	if dedup.BlobID == nil || *dedup.BlobID != blobSecond.ID {
		t.Fatalf("dedup blob = %v, want %d", dedup.BlobID, blobSecond.ID)
	}
	var rows int
	if err := e.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM blobs WHERE sha256 = ?`, sha).Scan(&rows); err != nil {
		t.Fatalf("count blobs: %v", err)
	}
	if rows != 2 {
		t.Fatalf("blob rows = %d, want 2", rows)
	}
}

// TestUploadInheritsParentPolicy: a child directory created under a second
// policy root keeps that policy, and the upload record remembers it.
func TestUploadInheritsParentPolicy(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	second, secondDir := e.secondRoot(t, "second", true)

	p := e.principal()
	sub, err := e.files.CreateDir(ctx, p, secondDir, "nested", nil)
	if err != nil {
		t.Fatalf("create nested: %v", err)
	}
	if sub.StoragePolicyID == nil || *sub.StoragePolicyID != second.ID {
		t.Fatalf("nested policy = %v, want inherited %d", sub.StoragePolicyID, second.ID)
	}

	res, err := e.svc.Init(ctx, p, sub, InitRequest{Name: "deep.txt", Size: 4})
	if err != nil {
		t.Fatalf("init: %v", err)
	}
	up, err := e.db.UploadByID(ctx, access.UserScope(e.userID, false), res.UploadID)
	if err != nil {
		t.Fatalf("upload row: %v", err)
	}
	if up.StoragePolicyID != second.ID {
		t.Fatalf("upload policy = %d, want %d", up.StoragePolicyID, second.ID)
	}

	node := e.upload(t, sub, "deep2.txt", "deep")
	if node.StoragePolicyID == nil || *node.StoragePolicyID != second.ID {
		t.Fatalf("node policy = %v, want %d", node.StoragePolicyID, second.ID)
	}
}

// TestUploadRejectsDisabledPolicy: starting an upload into a disabled root is
// an insufficient-storage error (507) with a dedicated reason, not a 500.
func TestUploadRejectsDisabledPolicy(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	second, secondDir := e.secondRoot(t, "second", true)

	if _, err := e.db.UpdateStoragePolicy(ctx, second.ID, sqlstore.StoragePolicyUpdate{Enabled: boolPtr(false)}); err != nil {
		t.Fatalf("disable: %v", err)
	}

	_, err := e.svc.Init(ctx, e.principal(), secondDir, InitRequest{Name: "nope.txt", Size: 1})
	if apperr.CodeOf(err) != apperr.CodeInsufficientStorage {
		t.Fatalf("init into disabled policy err = %v, want insufficient_storage", err)
	}
	if appErr, ok := apperr.From(err); !ok || appErr.Reason != apperr.ReasonStoragePolicyDisabled {
		t.Fatalf("reason = %v, want %q", err, apperr.ReasonStoragePolicyDisabled)
	}

	// Reads still work for content already stored in that root: only writes
	// are refused. (No node exists yet here, so check the policy lookup.)
	store, err := e.files.Store(ctx, secondDir)
	if err != nil {
		t.Fatalf("store for disabled policy: %v", err)
	}
	if store == nil {
		t.Fatal("no store for disabled policy")
	}
}

func boolPtr(v bool) *bool { return &v }
