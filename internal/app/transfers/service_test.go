package transfers

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"rapig/internal/domain/access"
	domainfiles "rapig/internal/domain/files"
	domainidentity "rapig/internal/domain/identity"
	"rapig/internal/domain/transfer"
	"rapig/internal/infra/fetch"
	"rapig/internal/infra/sql"
	storagereg "rapig/internal/infra/storage"
	"rapig/internal/infra/storage/local"
	"rapig/migrations"
)

type env struct {
	db        *sqlstore.DB
	store     *local.Store
	registry  *storagereg.Registry
	svc       *Service
	log       *slog.Logger
	dir       string
	userID    int64
	root      domainfiles.Node
	principal access.Principal
}

func newEnv(t *testing.T) *env {
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
	policyID, err := db.EnsureDefaultStoragePolicy(ctx, filepath.Join(dir, "storage"))
	if err != nil {
		t.Fatalf("policy: %v", err)
	}
	store, err := local.New(filepath.Join(dir, "storage"))
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	registry := storagereg.NewRegistry(db, log)
	roleID, err := db.RoleIDByCode(ctx, "user")
	if err != nil {
		t.Fatalf("role: %v", err)
	}
	user, err := db.CreateUser(ctx, domainidentity.User{
		Username: "alice", UsernameFolded: "alice", DisplayName: "Alice",
		PasswordHash: "x", RoleID: roleID, Status: domainidentity.StatusActive,
	})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	if _, err := db.EnsureRoot(ctx, user.ID, policyID); err != nil {
		t.Fatalf("ensure root: %v", err)
	}
	sc := access.UserScope(user.ID, false)
	root, err := db.RootNode(ctx, sc)
	if err != nil {
		t.Fatalf("root: %v", err)
	}
	perms := access.NewPermSet(access.ActionRead, access.ActionWrite, access.ActionCreate, access.ActionUpload)
	principal := access.Principal{UserID: user.ID, Username: "alice", Role: "user", Perms: perms}
	return &env{
		db: db, store: store, registry: registry, svc: New(db, registry, fetch.New(true), log, 2),
		log: log, dir: dir, userID: user.ID, root: root, principal: principal,
	}
}

func shaHex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func TestOfflineDownloadCompletesAndDedupes(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	body := []byte("offline payload")
	src := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(body)
	}))
	defer src.Close()

	tr, err := e.svc.Create(ctx, e.principal, e.root, "dl.bin", src.URL)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if tr.State != transfer.StatePending {
		t.Fatalf("initial state = %s", tr.State)
	}
	e.svc.RunPending(ctx)
	e.svc.Wait()

	sc := access.UserScope(e.userID, false)
	got, err := e.db.TransferByID(ctx, sc, tr.ID)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got.State != transfer.StateCompleted {
		t.Fatalf("state = %s (error %q)", got.State, got.Error)
	}
	if got.NodeID == nil {
		t.Fatal("completed transfer has no node")
	}
	if got.Size != int64(len(body)) {
		t.Fatalf("size = %d, want %d", got.Size, len(body))
	}
	node, sha, _, err := e.db.FileContent(ctx, sc, *got.NodeID)
	if err != nil {
		t.Fatalf("content: %v", err)
	}
	if node.Name != "dl.bin" {
		t.Fatalf("node name = %q", node.Name)
	}
	if sha != shaHex(body) {
		t.Fatalf("sha = %q, want %q", sha, shaHex(body))
	}

	// A second job with identical content dedupes onto the same blob.
	if _, err := e.svc.Create(ctx, e.principal, e.root, "dl2.bin", src.URL); err != nil {
		t.Fatalf("create second: %v", err)
	}
	e.svc.RunPending(ctx)
	e.svc.Wait()
	blobs, err := e.db.AllBlobs(ctx)
	if err != nil {
		t.Fatalf("blobs: %v", err)
	}
	if len(blobs) != 1 || blobs[0].Refcount != 2 {
		t.Fatalf("blobs = %+v, want one blob with refcount 2", blobs)
	}
}

func TestOfflineDownloadQuotaFailure(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	src := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("0123456789"))
	}))
	defer src.Close()

	if _, err := e.db.ExecContext(ctx, `UPDATE users SET quota_bytes = 4, used_bytes = 0 WHERE id = ?`, e.userID); err != nil {
		t.Fatalf("set quota: %v", err)
	}
	if _, err := e.svc.Create(ctx, e.principal, e.root, "big.bin", src.URL); err != nil {
		t.Fatalf("create: %v", err)
	}
	e.svc.RunPending(ctx)
	e.svc.Wait()

	list, _, err := e.svc.List(ctx, e.principal, "", 50, 0)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("transfers = %d", len(list))
	}
	if list[0].State != transfer.StateFailed {
		t.Fatalf("state = %s, want failed", list[0].State)
	}
	if list[0].Error == "" {
		t.Fatal("failed transfer should record an error message")
	}
}

func TestCreateRejectsPrivateURLWhenNotAllowed(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	if _, err := e.db.ExecContext(ctx, `UPDATE users SET quota_bytes = 0 WHERE id = ?`, e.userID); err != nil {
		t.Fatalf("quota: %v", err)
	}
	svc := New(e.db, e.registry, fetch.New(false), slog.New(slog.NewTextHandler(io.Discard, nil)), 1)
	if _, err := svc.Create(ctx, e.principal, e.root, "x.bin", "http://127.0.0.1:9/x"); err == nil {
		t.Fatal("expected private address to be rejected at create time")
	}
}
