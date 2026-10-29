package shares

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"log/slog"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	filessvc "rapig/internal/app/files"
	"rapig/internal/domain/access"
	domaingroups "rapig/internal/domain/groups"
	domainidentity "rapig/internal/domain/identity"
	"rapig/internal/domain/share"
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
	dirID  int64
	fileID int64
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
	roleID, err := db.RoleIDByCode(ctx, "user")
	if err != nil {
		t.Fatalf("role: %v", err)
	}
	user, err := db.CreateUser(ctx, domainidentity.User{
		Username: "alice", UsernameFolded: "alice", DisplayName: "Alice",
		PasswordHash: "x", RoleID: roleID, Status: domainidentity.StatusActive,
	})
	if err != nil {
		t.Fatalf("user: %v", err)
	}
	e := &env{db: db, store: store, files: fls, svc: New(db, fls, log), userID: user.ID, policy: policyID}
	sc := access.UserScope(user.ID, false)
	root, err := db.EnsureRoot(ctx, user.ID, policyID)
	if err != nil {
		t.Fatalf("root: %v", err)
	}
	dirNode, err := db.CreateDir(ctx, sc, root.ID, "docs", "docs")
	if err != nil {
		t.Fatalf("dir: %v", err)
	}
	e.dirID = dirNode.ID
	e.fileID = e.putFile(t, dirNode.ID, "docs/a.txt", "hello world")
	return e
}

func (e *env) putFile(t *testing.T, parentID int64, relPath, content string) int64 {
	t.Helper()
	ctx := context.Background()
	sc := access.UserScope(e.userID, false)
	sum := sha256.Sum256([]byte(content))
	sha := hex.EncodeToString(sum[:])
	if _, _, err := e.store.Put(ctx, strings.NewReader(content)); err != nil {
		t.Fatalf("store put: %v", err)
	}
	blob, _, err := e.db.InsertBlob(ctx, sha, int64(len(content)))
	if err != nil {
		t.Fatalf("blob: %v", err)
	}
	n, err := e.db.CommitCompletedUpload(ctx, sc, sqlstore.CompletedUploadInput{
		ParentID: parentID, Name: "a.txt", Path: relPath, Mime: "text/plain",
		SHA256: sha, Size: int64(len(content)), BlobID: blob.ID, CreatedAt: time.Now(),
	})
	if err != nil {
		t.Fatalf("commit: %v", err)
	}
	return n.ID
}

func (e *env) principal() access.Principal {
	return access.Principal{
		UserID: e.userID, Username: "alice", Role: "user",
		Perms: access.NewPermSet(access.AllActions...),
	}
}

func TestShareMetaTicketAndPassword(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	p := e.principal()

	plain, err := e.svc.Create(ctx, p, CreateRequest{NodeID: e.fileID})
	if err != nil {
		t.Fatalf("create plain: %v", err)
	}
	if plain.ExpiresAt == nil || time.Until(*plain.ExpiresAt) < 6*24*time.Hour {
		t.Fatalf("default expiry = %v, want ~7d", plain.ExpiresAt)
	}
	meta, err := e.svc.Meta(ctx, plain.Token, "")
	if err != nil || meta.Locked || meta.Node.Name != "a.txt" {
		t.Fatalf("meta = %+v %v", meta, err)
	}

	locked, err := e.svc.Create(ctx, p, CreateRequest{NodeID: e.fileID, Password: "abcd1234"})
	if err != nil {
		t.Fatalf("create locked: %v", err)
	}
	meta, err = e.svc.Meta(ctx, locked.Token, "")
	if err != nil || !meta.Locked {
		t.Fatalf("locked meta = %+v %v", meta, err)
	}
	if _, err := e.svc.Verify(ctx, locked.Token, "wrong-pass", "10.0.0.1"); apperr.CodeOf(err) != apperr.CodeInvalidCredentials {
		t.Fatalf("wrong password = %v, want invalid_credentials", err)
	}
	res, err := e.svc.Verify(ctx, locked.Token, "abcd1234", "10.0.0.1")
	if err != nil || res.Ticket == "" {
		t.Fatalf("verify = %+v %v", res, err)
	}
	meta, err = e.svc.Meta(ctx, locked.Token, res.Ticket)
	if err != nil || meta.Locked {
		t.Fatalf("unlocked meta = %+v %v", meta, err)
	}
	meta, _ = e.svc.Meta(ctx, locked.Token, res.Ticket+"tampered")
	if !meta.Locked {
		t.Fatal("tampered ticket was accepted")
	}
}

func TestShareExpiryRevokeAndBudget(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	p := e.principal()

	oneHour := int64(3600)
	sh, err := e.svc.Create(ctx, p, CreateRequest{NodeID: e.fileID, ExpiresIn: &oneHour, MaxDownloads: 1})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := e.svc.Consume(ctx, sh); err != nil {
		t.Fatalf("consume 1: %v", err)
	}
	if err := e.svc.Consume(ctx, sh); apperr.CodeOf(err) != apperr.CodeShareExhausted {
		t.Fatalf("consume 2 = %v, want share_exhausted", err)
	}

	forever := int64(0)
	perm, err := e.svc.Create(ctx, p, CreateRequest{NodeID: e.fileID, ExpiresIn: &forever})
	if err != nil || perm.ExpiresAt != nil {
		t.Fatalf("permanent create = %+v %v", perm, err)
	}

	// Time travel: the share created above expires one hour after creation.
	e.svc.now = func() time.Time { return time.Now().Add(2 * time.Hour) }
	if _, err := e.svc.Meta(ctx, sh.Token, ""); apperr.CodeOf(err) != apperr.CodeNotFound {
		t.Fatalf("expired meta = %v, want not_found", err)
	}
	if err := e.svc.Revoke(ctx, p, perm.ID); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if _, err := e.svc.Meta(ctx, perm.Token, ""); apperr.CodeOf(err) != apperr.CodeNotFound {
		t.Fatalf("revoked meta = %v, want not_found", err)
	}
}

func TestShareDirectorySubtreeAndBoundary(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	p := e.principal()
	dirShare, err := e.svc.Create(ctx, p, CreateRequest{NodeID: e.dirID})
	if err != nil {
		t.Fatalf("create dir share: %v", err)
	}
	_, current, items, total, err := e.svc.Nodes(ctx, dirShare.Token, "", 0, 50, 0)
	if err != nil || current.ID != e.dirID || total != 1 || len(items) != 1 || items[0].Name != "a.txt" {
		t.Fatalf("nodes = %+v %d %d %v", current, len(items), total, err)
	}
	if _, _, _, _, err := e.svc.Content(ctx, dirShare.Token, "", e.fileID); err != nil {
		t.Fatalf("content inside subtree: %v", err)
	}

	outside := e.putFile(t, e.rootID(t), "outside.txt", "secret")
	if _, _, _, _, err := e.svc.Content(ctx, dirShare.Token, "", outside); apperr.CodeOf(err) != apperr.CodeNotFound {
		t.Fatalf("content outside subtree = %v, want not_found", err)
	}
	if _, _, _, _, err := e.svc.Content(ctx, dirShare.Token, "", e.dirID); apperr.CodeOf(err) != apperr.CodeBadRequest {
		t.Fatalf("content of a directory = %v, want bad_request", err)
	}
}

func (e *env) rootID(t *testing.T) int64 {
	t.Helper()
	ctx := context.Background()
	root, err := e.db.RootNode(ctx, access.UserScope(e.userID, false))
	if err != nil {
		t.Fatalf("root: %v", err)
	}
	return root.ID
}

func TestShareVerifyRateLimit(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	sh, err := e.svc.Create(ctx, e.principal(), CreateRequest{NodeID: e.fileID, Password: "abcd1234"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	for i := 0; i < verifyMaxFails; i++ {
		if _, err := e.svc.Verify(ctx, sh.Token, "wrong", "10.9.9.9"); apperr.CodeOf(err) != apperr.CodeInvalidCredentials {
			t.Fatalf("attempt %d = %v", i, err)
		}
	}
	_, err = e.svc.Verify(ctx, sh.Token, "abcd1234", "10.9.9.9")
	if apperr.CodeOf(err) != apperr.CodeTooManyAttempts {
		t.Fatalf("blocked verify = %v, want too_many_attempts", err)
	}
	if e, ok := apperr.From(err); ok && e.RetryAfter <= 0 {
		t.Fatalf("blocked verify retry_after = %d, want >0", e.RetryAfter)
	}
}

func TestMergedShareNodesAndRootBan(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	p := e.principal()
	sc := access.UserScope(e.userID, false)
	second := e.putFileNamed(t, e.dirID, "b.txt", "docs/b.txt", "second file")

	sh, err := e.svc.Create(ctx, p, CreateRequest{NodeIDs: []int64{e.fileID, e.dirID}})
	if err != nil {
		t.Fatalf("create merged: %v", err)
	}
	if !sh.Multi() || sh.ItemCount() != 2 || sh.Kind != "multi" {
		t.Fatalf("merged share = %+v", sh)
	}
	meta, err := e.svc.Meta(ctx, sh.Token, "")
	if err != nil || meta.ItemCount != 2 || meta.Node.ID != e.fileID {
		t.Fatalf("meta = %+v %v", meta, err)
	}

	_, parent, items, total, err := e.svc.Nodes(ctx, sh.Token, "", 0, 50, 0)
	if err != nil || total != 2 || parent.ID != 0 || len(items) != 2 {
		t.Fatalf("root items = %+v %d %v", items, total, err)
	}

	_, parent, items, total, err = e.svc.Nodes(ctx, sh.Token, "", e.dirID, 50, 0)
	if err != nil || parent.ID != e.dirID || total != 2 || len(items) != 2 {
		t.Fatalf("dir listing = %+v %d %v", items, total, err)
	}

	if _, _, sha, _, err := e.svc.Content(ctx, sh.Token, "", second); err != nil || sha == "" {
		t.Fatalf("content second = %q %v", sha, err)
	}

	root, err := e.db.RootNode(ctx, sc)
	if err != nil {
		t.Fatalf("root: %v", err)
	}
	otherDir, err := e.db.CreateDir(ctx, sc, root.ID, "other", "other")
	if err != nil {
		t.Fatalf("other dir: %v", err)
	}
	if _, _, _, _, err := e.svc.Content(ctx, sh.Token, "", otherDir.ID); apperr.CodeOf(err) != apperr.CodeNotFound {
		t.Fatalf("outside content = %v, want not_found", err)
	}
	if _, err := e.svc.Create(ctx, p, CreateRequest{NodeIDs: []int64{root.ID}}); apperr.CodeOf(err) != apperr.CodeInvalidTarget {
		t.Fatalf("root share = %v, want invalid_target", err)
	}

	if err := e.svc.Revoke(ctx, p, sh.ID); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if list, total, err := e.svc.List(ctx, p, "revoked", 50, 0); err != nil || total != 1 || !list[0].Multi() {
		t.Fatalf("revoked list = %d %v", total, err)
	}
	if _, err := e.svc.Restore(ctx, p, sh.ID); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if err := e.svc.Purge(ctx, p, sh.ID); apperr.CodeOf(err) != apperr.CodeInvalidState {
		t.Fatalf("purge active = %v, want invalid_state", err)
	}
	if err := e.svc.Revoke(ctx, p, sh.ID); err != nil {
		t.Fatalf("re-revoke: %v", err)
	}
	if err := e.svc.Purge(ctx, p, sh.ID); err != nil {
		t.Fatalf("purge: %v", err)
	}
	if _, err := e.svc.Meta(ctx, sh.Token, ""); apperr.CodeOf(err) != apperr.CodeNotFound {
		t.Fatalf("meta after purge = %v, want not_found", err)
	}
}

func (e *env) putFileNamed(t *testing.T, parentID int64, name, relPath, content string) int64 {
	t.Helper()
	ctx := context.Background()
	sc := access.UserScope(e.userID, false)
	sum := sha256.Sum256([]byte(content))
	sha := hex.EncodeToString(sum[:])
	if _, _, err := e.store.Put(ctx, strings.NewReader(content)); err != nil {
		t.Fatalf("store put: %v", err)
	}
	blob, _, err := e.db.InsertBlob(ctx, sha, int64(len(content)))
	if err != nil {
		t.Fatalf("blob: %v", err)
	}
	n, err := e.db.CommitCompletedUpload(ctx, sc, sqlstore.CompletedUploadInput{
		ParentID: parentID, Name: name, Path: relPath, Mime: "text/plain",
		SHA256: sha, Size: int64(len(content)), BlobID: blob.ID, CreatedAt: time.Now(),
	})
	if err != nil {
		t.Fatalf("commit: %v", err)
	}
	return n.ID
}

func TestCreateRejectsTooManyItems(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	p := e.principal()
	sc := access.UserScope(e.userID, false)
	root, err := e.db.RootNode(ctx, sc)
	if err != nil {
		t.Fatalf("root: %v", err)
	}
	ids := make([]int64, 0, share.MaxItems+1)
	for i := 0; i <= share.MaxItems; i++ {
		name := "cap-" + strconv.Itoa(i)
		n, err := e.db.CreateDir(ctx, sc, root.ID, name, name)
		if err != nil {
			t.Fatalf("create dir %d: %v", i, err)
		}
		ids = append(ids, n.ID)
	}
	_, err = e.svc.Create(ctx, p, CreateRequest{NodeIDs: ids})
	if apperr.CodeOf(err) != apperr.CodeInvalidTarget {
		t.Fatalf("create with %d items = %v, want invalid_target", len(ids), err)
	}
	appErr, _ := apperr.From(err)
	if appErr.Reason != apperr.ReasonTooManyItems {
		t.Fatalf("reason = %q, want %q", appErr.Reason, apperr.ReasonTooManyItems)
	}
}

// TestGroupSharePublicAccess covers a share whose node lives in a group space:
// the anonymous token must resolve through the group scope, and the subtree
// boundary must not leak into the owner's personal space.
func TestGroupSharePublicAccess(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()

	g, groupRootID, err := e.db.CreateGroup(ctx, domaingroups.Group{
		Name: "team", NameFolded: "team", CreatedBy: &e.userID,
		MemberPerms: []access.Action{access.ActionRead},
	}, e.userID)
	if err != nil {
		t.Fatalf("create group: %v", err)
	}
	gsc := access.UserGroupScope(e.userID, false, []int64{g.ID})
	root, err := e.db.GroupRootNode(ctx, gsc, g.ID)
	if err != nil || root.ID != groupRootID {
		t.Fatalf("group root = %v %v", root, err)
	}
	dir, err := e.db.CreateDir(ctx, gsc, root.ID, "docs", "docs")
	if err != nil {
		t.Fatalf("group dir: %v", err)
	}
	sum := sha256.Sum256([]byte("group payload"))
	sha := hex.EncodeToString(sum[:])
	if _, _, err := e.store.Put(ctx, strings.NewReader("group payload")); err != nil {
		t.Fatalf("store put: %v", err)
	}
	blob, _, err := e.db.InsertBlob(ctx, sha, int64(len("group payload")))
	if err != nil {
		t.Fatalf("blob: %v", err)
	}
	gfile, err := e.db.CommitCompletedUpload(ctx, gsc, sqlstore.CompletedUploadInput{
		ParentID: dir.ID, Name: "g.txt", Path: "docs/g.txt", Mime: "text/plain",
		SHA256: sha, Size: int64(len("group payload")), BlobID: blob.ID, CreatedAt: time.Now(),
	})
	if err != nil {
		t.Fatalf("group upload: %v", err)
	}

	p := e.principal()
	p.GroupRoles = map[int64]access.GroupRoleInfo{
		g.ID: {Role: access.GroupRoleManager, Perms: access.NewPermSet(access.AllActions...)},
	}
	fileShare, err := e.svc.Create(ctx, p, CreateRequest{NodeID: gfile.ID})
	if err != nil {
		t.Fatalf("create file share: %v", err)
	}
	meta, err := e.svc.Meta(ctx, fileShare.Token, "")
	if err != nil || meta.Node.Name != "g.txt" {
		t.Fatalf("file meta = %+v %v", meta, err)
	}
	_, node, gotSHA, mime, err := e.svc.Content(ctx, fileShare.Token, "", 0)
	if err != nil || node.ID != gfile.ID || gotSHA != sha || mime != "text/plain" {
		t.Fatalf("content = %+v %q %q %v", node, gotSHA, mime, err)
	}

	dirShare, err := e.svc.Create(ctx, p, CreateRequest{NodeID: dir.ID})
	if err != nil {
		t.Fatalf("create dir share: %v", err)
	}
	_, parent, items, total, err := e.svc.Nodes(ctx, dirShare.Token, "", 0, 50, 0)
	if err != nil || total != 1 || len(items) != 1 || items[0].Name != "g.txt" || parent.ID != dir.ID {
		t.Fatalf("group listing = %+v %d %v", items, total, err)
	}
	// A node from the personal space must stay invisible even with the token.
	if _, _, _, _, err := e.svc.Nodes(ctx, dirShare.Token, "", e.fileID, 50, 0); apperr.CodeOf(err) != apperr.CodeNotFound {
		t.Fatalf("cross-space lookup = %v, want not_found", err)
	}
}
