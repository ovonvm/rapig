package files

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"testing"

	"rapig/internal/domain/access"
	domainfiles "rapig/internal/domain/files"
	domainidentity "rapig/internal/domain/identity"
	"rapig/internal/infra/sql"
	storagereg "rapig/internal/infra/storage"
	"rapig/internal/platform/apperr"
	"rapig/migrations"
)

type policyEnv struct {
	db     *sqlstore.DB
	svc    *Service
	p      access.Principal
	userID int64
	root   domainfiles.Node
	first  int64
}

func newPolicyEnv(t *testing.T) *policyEnv {
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
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	svc := NewService(db, storagereg.NewRegistry(db, log), log)
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
	p := access.Principal{
		UserID: user.ID, Username: "alice", Role: "user",
		Perms: access.NewPermSet(access.AllActions...),
	}
	root, err := svc.Parent(ctx, p, 0)
	if err != nil {
		t.Fatalf("root: %v", err)
	}
	return &policyEnv{db: db, svc: svc, p: p, userID: user.ID, root: root, first: policyID}
}

// secondPolicy registers another root and returns a directory inside it.
func (e *policyEnv) secondPolicy(t *testing.T, name string) (int64, domainfiles.Node) {
	t.Helper()
	ctx := context.Background()
	policy, err := e.db.CreateStoragePolicy(ctx, sqlstore.NewStoragePolicy{
		Name: name, RootPath: filepath.Join(t.TempDir(), "root"), Enabled: true,
	})
	if err != nil {
		t.Fatalf("create policy: %v", err)
	}
	node, err := e.svc.CreateDir(ctx, e.p, e.root, name, &policy.ID)
	if err != nil {
		t.Fatalf("create dir: %v", err)
	}
	return policy.ID, node
}

// TestCrossPolicyMoveAndCopyAreRejected pins the new 409 reason: a node may not
// travel to a directory backed by another root, because its bytes live there.
func TestCrossPolicyMoveAndCopyAreRejected(t *testing.T) {
	ctx := context.Background()
	e := newPolicyEnv(t)
	_, secondDir := e.secondPolicy(t, "second")

	src, err := e.svc.CreateDir(ctx, e.p, e.root, "src", nil)
	if err != nil {
		t.Fatalf("create src: %v", err)
	}
	if src.StoragePolicyID == nil || *src.StoragePolicyID != e.first {
		t.Fatalf("src policy = %v, want %d", src.StoragePolicyID, e.first)
	}

	target := secondDir.ID
	_, err = e.svc.Move(ctx, e.p, src, &target, nil, fmt.Sprint(src.Rev))
	if apperr.CodeOf(err) != apperr.CodeInvalidState {
		t.Fatalf("cross-policy move err = %v, want 409 invalid_state", err)
	}
	if appErr, ok := apperr.From(err); !ok || appErr.Reason != apperr.ReasonCrossPolicyOperation {
		t.Fatalf("move reason = %v, want %q", err, apperr.ReasonCrossPolicyOperation)
	}

	_, err = e.svc.Copy(ctx, e.p, src.ID, &target, nil)
	if appErr, ok := apperr.From(err); !ok || appErr.Reason != apperr.ReasonCrossPolicyOperation {
		t.Fatalf("copy reason = %v, want %q", err, apperr.ReasonCrossPolicyOperation)
	}

	// The node did not move.
	reloaded, err := e.svc.Node(ctx, e.p, src.ID)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if reloaded.ParentID == nil || *reloaded.ParentID != e.root.ID {
		t.Fatalf("parent = %v, want %d", reloaded.ParentID, e.root.ID)
	}
}

// TestSamePolicyMoveAndCopyStillWork: the guard only fires when the roots
// differ, so ordinary moves keep working (no storage rewrite).
func TestSamePolicyMoveAndCopyStillWork(t *testing.T) {
	ctx := context.Background()
	e := newPolicyEnv(t)

	dir, err := e.svc.CreateDir(ctx, e.p, e.root, "dest", nil)
	if err != nil {
		t.Fatalf("create dest: %v", err)
	}
	fileDir, err := e.svc.CreateDir(ctx, e.p, e.root, "src", nil)
	if err != nil {
		t.Fatalf("create src: %v", err)
	}

	target := dir.ID
	moved, err := e.svc.Move(ctx, e.p, fileDir, &target, nil, fmt.Sprint(fileDir.Rev))
	if err != nil {
		t.Fatalf("same-policy move: %v", err)
	}
	if moved.ParentID == nil || *moved.ParentID != dir.ID {
		t.Fatalf("moved parent = %v, want %d", moved.ParentID, dir.ID)
	}
	if moved.StoragePolicyID == nil || *moved.StoragePolicyID != e.first {
		t.Fatalf("moved policy = %v, want %d", moved.StoragePolicyID, e.first)
	}

	copied, err := e.svc.Copy(ctx, e.p, moved.ID, nil, strPtr("src-copy"))
	if err != nil {
		t.Fatalf("same-policy copy: %v", err)
	}
	if copied.StoragePolicyID == nil || *copied.StoragePolicyID != e.first {
		t.Fatalf("copied policy = %v, want %d", copied.StoragePolicyID, e.first)
	}
	if copied.Name != "src-copy" {
		t.Fatalf("copied name = %q", copied.Name)
	}
}

// TestSecondPolicyRootIsIndependent: a directory created at the root with an
// explicit policy keeps it, and children inherit it (the M5.3 inheritance rule).
func TestSecondPolicyRootIsIndependent(t *testing.T) {
	ctx := context.Background()
	e := newPolicyEnv(t)
	secondID, secondDir := e.secondPolicy(t, "second")

	child, err := e.svc.CreateDir(ctx, e.p, secondDir, "child", nil)
	if err != nil {
		t.Fatalf("create child: %v", err)
	}
	if child.StoragePolicyID == nil || *child.StoragePolicyID != secondID {
		t.Fatalf("child policy = %v, want inherited %d", child.StoragePolicyID, secondID)
	}

	// The root itself stays on the default policy.
	if e.root.StoragePolicyID == nil || *e.root.StoragePolicyID != e.first {
		t.Fatalf("root policy = %v, want %d", e.root.StoragePolicyID, e.first)
	}
}

func strPtr(s string) *string { return &s }
