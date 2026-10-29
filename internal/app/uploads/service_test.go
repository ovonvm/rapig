package uploads

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	filessvc "rapig/internal/app/files"
	"rapig/internal/domain/access"
	domainfiles "rapig/internal/domain/files"
	domainidentity "rapig/internal/domain/identity"
	"rapig/internal/infra/sql"
	storagereg "rapig/internal/infra/storage"
	"rapig/internal/infra/storage/local"
	"rapig/internal/platform/apperr"
	"rapig/migrations"
)

type env struct {
	db     *sqlstore.DB
	store  *local.Store
	files  *filessvc.Service
	svc    *Service
	userID int64
	policy int64
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
	fls := filessvc.NewService(db, registry, log)
	svc := New(db, registry, fls, log, 2, 64<<10)
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
	return &env{db: db, store: store, files: fls, svc: svc, userID: user.ID, policy: policyID}
}

func (e *env) principal() access.Principal {
	return access.Principal{
		UserID: e.userID, Username: "alice", Role: "user",
		Perms: access.NewPermSet(access.AllActions...),
	}
}

func (e *env) root(t *testing.T) domainfiles.Node {
	t.Helper()
	n, err := e.files.Parent(context.Background(), e.principal(), 0)
	if err != nil {
		t.Fatalf("root: %v", err)
	}
	return n
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func TestUploadLifecycle(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	p := e.principal()
	root := e.root(t)

	res, err := e.svc.Init(ctx, p, root, InitRequest{Name: "hello.txt", Size: 5})
	if err != nil {
		t.Fatalf("init: %v", err)
	}
	if res.UploadID == "" || res.PartsTotal != 1 || res.PartSize == 0 {
		t.Fatalf("unexpected init result: %+v", res)
	}
	if err := e.svc.PutPart(ctx, p, res.UploadID, 0, strings.NewReader("hello")); err != nil {
		t.Fatalf("put part: %v", err)
	}
	node, err := e.svc.Complete(ctx, p, res.UploadID, map[int64]string{0: sha256Hex([]byte("hello"))})
	if err != nil {
		t.Fatalf("complete: %v", err)
	}
	if node.Name != "hello.txt" || node.Size != 5 || node.BlobID == nil {
		t.Fatalf("unexpected node: %+v", node)
	}
	used, reserved, err := e.db.UserQuota(ctx, e.userID)
	if err != nil {
		t.Fatalf("quota: %v", err)
	}
	if used != 5 || reserved != 0 {
		t.Fatalf("quota after complete: used=%d reserved=%d", used, reserved)
	}
	objPath, err := e.store.Path(sha256Hex([]byte("hello")))
	if err != nil {
		t.Fatalf("store path: %v", err)
	}
	if _, err := os.Stat(objPath); err != nil {
		t.Fatalf("object missing: %v", err)
	}
}

func TestInstantUploadDedupsContent(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	p := e.principal()
	root := e.root(t)
	content := []byte("deduplicated bytes")
	sum := sha256Hex(content)

	res, err := e.svc.Init(ctx, p, root, InitRequest{Name: "a.bin", Size: int64(len(content)), SHA256: sum})
	if err != nil {
		t.Fatalf("init: %v", err)
	}
	if err := e.svc.PutPart(ctx, p, res.UploadID, 0, strings.NewReader(string(content))); err != nil {
		t.Fatalf("put: %v", err)
	}
	if _, err := e.svc.Complete(ctx, p, res.UploadID, nil); err != nil {
		t.Fatalf("complete: %v", err)
	}

	instant, err := e.svc.Init(ctx, p, root, InitRequest{Name: "b.bin", Size: int64(len(content)), SHA256: sum})
	if err != nil {
		t.Fatalf("instant init: %v", err)
	}
	if instant.DedupedNodeID == nil {
		t.Fatalf("expected instant upload, got %+v", instant)
	}
	used, reserved, err := e.db.UserQuota(ctx, e.userID)
	if err != nil {
		t.Fatalf("quota: %v", err)
	}
	if used != int64(2*len(content)) || reserved != 0 {
		t.Fatalf("quota after dedup: used=%d reserved=%d", used, reserved)
	}
	var refcount int
	if err := e.db.QueryRowContext(ctx, `SELECT refcount FROM blobs WHERE sha256 = ?`, sum).Scan(&refcount); err != nil {
		t.Fatalf("refcount: %v", err)
	}
	if refcount != 2 {
		t.Fatalf("refcount = %d, want 2", refcount)
	}
}

func TestUploadChecksumMismatch(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	p := e.principal()
	root := e.root(t)
	res, err := e.svc.Init(ctx, p, root, InitRequest{Name: "x.bin", Size: 5, SHA256: sha256Hex([]byte("hello"))})
	if err != nil {
		t.Fatalf("init: %v", err)
	}
	if err := e.svc.PutPart(ctx, p, res.UploadID, 0, strings.NewReader("HELLO")); err != nil {
		t.Fatalf("put: %v", err)
	}
	_, err = e.svc.Complete(ctx, p, res.UploadID, nil)
	if code := apperr.CodeOf(err); code != apperr.CodeChecksumMismatch {
		t.Fatalf("complete error = %v (code %s), want checksum_mismatch", err, code)
	}
}

func TestCancelReleasesQuota(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	p := e.principal()
	root := e.root(t)
	res, err := e.svc.Init(ctx, p, root, InitRequest{Name: "big.bin", Size: 1 << 20})
	if err != nil {
		t.Fatalf("init: %v", err)
	}
	if _, reserved, _ := e.db.UserQuota(ctx, e.userID); reserved != 1<<20 {
		t.Fatalf("reserved = %d, want %d", reserved, 1<<20)
	}
	if err := e.svc.Cancel(ctx, p, res.UploadID); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	if _, reserved, _ := e.db.UserQuota(ctx, e.userID); reserved != 0 {
		t.Fatalf("reserved after cancel = %d, want 0", reserved)
	}
	if err := e.svc.Cancel(ctx, p, res.UploadID); err != nil {
		t.Fatalf("second cancel must be a no-op: %v", err)
	}
}

func TestQuotaRejected(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	if _, err := e.db.ExecContext(ctx,
		`UPDATE users SET quota_bytes = 10 WHERE id = ?`, e.userID); err != nil {
		t.Fatalf("set quota: %v", err)
	}
	root := e.root(t)
	_, err := e.svc.Init(ctx, e.principal(), root, InitRequest{Name: "too.big", Size: 11})
	if code := apperr.CodeOf(err); code != apperr.CodeQuotaExceeded {
		t.Fatalf("init error = %v, want quota_exceeded", err)
	}
}

func TestCrossUserUploadIsNotFound(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	root := e.root(t)
	res, err := e.svc.Init(ctx, e.principal(), root, InitRequest{Name: "a.txt", Size: 1})
	if err != nil {
		t.Fatalf("init: %v", err)
	}
	stranger := access.Principal{UserID: e.userID + 1, Username: "bob", Role: "user",
		Perms: access.NewPermSet(access.AllActions...)}
	if _, err := e.svc.Complete(ctx, stranger, res.UploadID, nil); apperr.CodeOf(err) != apperr.CodeNotFound {
		t.Fatalf("stranger complete error = %v, want not_found", err)
	}
	if err := e.svc.Cancel(ctx, stranger, res.UploadID); apperr.CodeOf(err) != apperr.CodeNotFound {
		t.Fatalf("stranger cancel error = %v, want not_found", err)
	}
}
