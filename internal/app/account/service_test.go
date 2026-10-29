package account

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	adminsvc "rapig/internal/app/admin"
	idsvc "rapig/internal/app/identity"
	"rapig/internal/domain/access"
	"rapig/internal/domain/identity"
	sqlstore "rapig/internal/infra/sql"
	"rapig/internal/platform/apperr"
	"rapig/internal/platform/passwd"
	"rapig/migrations"
)

const (
	testPassword  = "alice-password-1"
	adminPassword = "admin-password-1"
)

type accountEnv struct {
	t      *testing.T
	db     *sqlstore.DB
	svc    *Service
	ids    *idsvc.Service
	admin  adminsvc.Service
	now    time.Time
	rootID int64
	alice  int64
	nodeID int64
}

func (e *accountEnv) advance(d time.Duration) { e.now = e.now.Add(d) }

func (e *accountEnv) count(query string, args ...any) int {
	e.t.Helper()
	var n int
	if err := e.db.QueryRowContext(context.Background(), query, args...).Scan(&n); err != nil {
		e.t.Fatalf("count %q: %v", query, err)
	}
	return n
}

func (e *accountEnv) exec(query string, args ...any) {
	e.t.Helper()
	if _, err := e.db.ExecContext(context.Background(), query, args...); err != nil {
		e.t.Fatalf("exec %q: %v", query, err)
	}
}

func alicePrincipal(id int64) access.Principal {
	return access.Principal{UserID: id, Username: "alice", Role: adminsvc.RoleUser, Perms: access.NewPermSet()}
}

func rootPrincipal(id int64) access.Principal {
	return access.Principal{
		UserID: id, Username: "root", Role: adminsvc.RoleAdmin,
		Perms: access.NewPermSet(access.ActionAdminUsers, access.ActionAdminSettings),
	}
}

func newAccountEnv(t *testing.T) *accountEnv {
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
		t.Fatalf("storage policy: %v", err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	adminRole, err := db.RoleIDByCode(ctx, adminsvc.RoleAdmin)
	if err != nil {
		t.Fatalf("admin role: %v", err)
	}
	userRole, err := db.RoleIDByCode(ctx, adminsvc.RoleUser)
	if err != nil {
		t.Fatalf("user role: %v", err)
	}
	adminHash, err := passwd.Hash(adminPassword)
	if err != nil {
		t.Fatalf("hash admin password: %v", err)
	}
	userHash, err := passwd.Hash(testPassword)
	if err != nil {
		t.Fatalf("hash user password: %v", err)
	}
	root, err := db.CreateUser(ctx, identity.User{
		Username: "root", UsernameFolded: "root", DisplayName: "root",
		PasswordHash: adminHash, RoleID: adminRole, Status: identity.StatusActive, IsRoot: true,
	})
	if err != nil {
		t.Fatalf("create root: %v", err)
	}
	alice, err := db.CreateUser(ctx, identity.User{
		Username: "alice", UsernameFolded: "alice", DisplayName: "alice",
		PasswordHash: userHash, RoleID: userRole, Status: identity.StatusActive,
	})
	if err != nil {
		t.Fatalf("create alice: %v", err)
	}
	rootNode, err := db.EnsureRoot(ctx, alice.ID, policyID)
	if err != nil {
		t.Fatalf("ensure root: %v", err)
	}
	// A second account keeps root from being the only active administrator, so
	// the last-admin guard has to be exercised explicitly.
	if _, err := db.CreateUser(ctx, identity.User{
		Username: "bob", UsernameFolded: "bob", DisplayName: "bob",
		PasswordHash: userHash, RoleID: userRole, Status: identity.StatusActive,
	}); err != nil {
		t.Fatalf("create bob: %v", err)
	}
	if err := db.SetSetting(ctx, sqlstore.SettingDeletionEnabled, "1"); err != nil {
		t.Fatalf("enable deletion: %v", err)
	}

	now := time.Now().UTC().Truncate(time.Second)
	env := &accountEnv{
		t: t, db: db, now: now, rootID: root.ID, alice: alice.ID, nodeID: rootNode.ID,
	}
	ids := idsvc.NewService(db, log)
	env.ids = ids
	env.svc = New(db, ids, log)
	env.svc.now = func() time.Time { return env.now }
	env.admin = *adminsvc.New(db, log)

	// Two live sessions, one active share and one pending transfer, all owned by
	// alice: the deletion must destroy every one of them.
	env.exec(`INSERT INTO sessions (id, user_id, csrf_hash, ip, user_agent, created_at, expires_at)
		VALUES ('sess-1', ?, 'csrf-1', '', '', ?, ?)`, alice.ID, now.Unix(), now.Add(24*time.Hour).Unix())
	env.exec(`INSERT INTO sessions (id, user_id, csrf_hash, ip, user_agent, created_at, expires_at)
		VALUES ('sess-2', ?, 'csrf-2', '', '', ?, ?)`, alice.ID, now.Unix(), now.Add(24*time.Hour).Unix())
	env.exec(`INSERT INTO shares (id, token, owner_id, node_id, kind, password_hash,
			max_downloads, download_count, visits_count, created_at)
		VALUES ('share-1', 'share-token-1', ?, ?, 'dir', '', 0, 0, 0, ?)`,
		alice.ID, rootNode.ID, now.Unix())
	env.exec(`INSERT INTO transfers (id, owner_id, parent_id, name, name_folded, source_url,
			size, received_bytes, state, error, created_at, updated_at)
		VALUES ('tr-1', ?, ?, 'f.bin', 'f.bin', 'http://example.invalid/f.bin',
			0, 0, 'pending', '', ?, ?)`, alice.ID, rootNode.ID, now.Unix(), now.Unix())
	return env
}

func TestDeletionDisabledByDefaultBlocksRequest(t *testing.T) {
	env := newAccountEnv(t)
	ctx := context.Background()
	if err := env.db.SetSetting(ctx, sqlstore.SettingDeletionEnabled, "0"); err != nil {
		t.Fatalf("disable deletion: %v", err)
	}
	enabled, err := env.svc.Enabled(ctx)
	if err != nil || enabled {
		t.Fatalf("Enabled = %v, %v; want false", enabled, err)
	}
	_, err = env.svc.RequestDeletion(ctx, alicePrincipal(env.alice), testPassword)
	if apperr.CodeOf(err) != apperr.CodeForbidden {
		t.Fatalf("code = %v, want forbidden (%v)", apperr.CodeOf(err), err)
	}
	if e, ok := apperr.From(err); !ok || e.Reason != apperr.ReasonDeletionDisabled {
		t.Fatalf("reason = %v, want deletion_disabled", err)
	}
}

func TestRootCannotRequestDeletion(t *testing.T) {
	env := newAccountEnv(t)
	_, err := env.svc.RequestDeletion(context.Background(), rootPrincipal(env.rootID), adminPassword)
	if apperr.CodeOf(err) != apperr.CodeForbidden {
		t.Fatalf("code = %v, want forbidden (%v)", apperr.CodeOf(err), err)
	}
	if e, ok := apperr.From(err); !ok || e.Reason != apperr.ReasonRootAdmin {
		t.Fatalf("reason = %v, want root_admin", err)
	}
}

func TestStatusOfReportsSwitchAndPendingDeletion(t *testing.T) {
	env := newAccountEnv(t)
	ctx := context.Background()
	p := alicePrincipal(env.alice)

	status, err := env.svc.StatusOf(ctx, p)
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if !status.Enabled || status.DeletedAt != nil || status.PurgeAfter != nil {
		t.Fatalf("unexpected status before deletion: %+v", status)
	}

	if _, err := env.svc.RequestDeletion(ctx, p, testPassword); err != nil {
		t.Fatalf("request deletion: %v", err)
	}
	status, err = env.svc.StatusOf(ctx, p)
	if err != nil {
		t.Fatalf("status after request: %v", err)
	}
	if !status.Enabled || status.DeletedAt == nil || status.PurgeAfter == nil {
		t.Fatalf("pending state not reported: %+v", status)
	}
	if !status.PurgeAfter.After(*status.DeletedAt) {
		t.Fatalf("purge_after %v is not after deleted_at %v", status.PurgeAfter, status.DeletedAt)
	}

	// Turning the switch off hides the feature but not the pending account.
	if err := env.db.SetSetting(ctx, sqlstore.SettingDeletionEnabled, "0"); err != nil {
		t.Fatalf("disable deletion: %v", err)
	}
	status, err = env.svc.StatusOf(ctx, p)
	if err != nil {
		t.Fatalf("status with switch off: %v", err)
	}
	if status.Enabled {
		t.Fatal("switch reported enabled after being turned off")
	}
	if status.DeletedAt == nil {
		t.Fatal("pending deletion vanished with the switch")
	}

	if _, err := env.svc.StatusOf(ctx, access.Principal{}); apperr.CodeOf(err) != apperr.CodeUnauthenticated {
		t.Fatalf("anonymous status code = %v, want unauthenticated", apperr.CodeOf(err))
	}
}

func TestRequestDeletionDisablesAccountAndRevokesAccess(t *testing.T) {
	env := newAccountEnv(t)
	ctx := context.Background()
	p := alicePrincipal(env.alice)

	res, err := env.svc.RequestDeletion(ctx, p, testPassword)
	if err != nil {
		t.Fatalf("request deletion: %v", err)
	}
	if len(res.CancelToken) < 32 {
		t.Fatalf("cancel token = %q (len %d), want >=32 chars", res.CancelToken, len(res.CancelToken))
	}
	wantPurge := env.now.Add(GracePeriod)
	if !res.PurgeAt.Equal(wantPurge) {
		t.Fatalf("purge_at = %s, want %s", res.PurgeAt, wantPurge)
	}

	user, err := env.db.UserByID(ctx, env.alice)
	if err != nil {
		t.Fatalf("reload user: %v", err)
	}
	if user.Status != identity.StatusDisabled {
		t.Fatalf("status = %q, want disabled", user.Status)
	}
	if user.DeletedAt == nil || !user.DeletedAt.Equal(env.now) {
		t.Fatalf("deleted_at = %v, want %s", user.DeletedAt, env.now)
	}
	if user.PurgeAfter == nil || !user.PurgeAfter.Equal(wantPurge) {
		t.Fatalf("purge_after = %v, want %s", user.PurgeAfter, wantPurge)
	}

	if n := env.count(`SELECT COUNT(*) FROM sessions WHERE user_id = ?`, env.alice); n != 0 {
		t.Errorf("sessions left = %d, want 0", n)
	}
	if got := env.count(`SELECT COUNT(*) FROM shares WHERE owner_id = ? AND revoked_at IS NULL`, env.alice); got != 0 {
		t.Errorf("active shares left = %d, want 0", got)
	}
	if got := env.count(`SELECT COUNT(*) FROM transfers WHERE owner_id = ? AND state IN ('pending','running','paused')`, env.alice); got != 0 {
		t.Errorf("active transfers left = %d, want 0", got)
	}
	var state string
	if err := env.db.QueryRowContext(ctx, `SELECT state FROM transfers WHERE id = 'tr-1'`).Scan(&state); err != nil {
		t.Fatalf("transfer state: %v", err)
	}
	if state != "canceled" {
		t.Errorf("transfer state = %q, want canceled", state)
	}

	// The stored token is the sha256 of the handed-out secret, live until purge.
	var hash string
	var expires int64
	var used *int64
	if err := env.db.QueryRowContext(ctx,
		`SELECT token_hash, expires_at, used_at FROM deletion_tokens WHERE user_id = ?`,
		env.alice).Scan(&hash, &expires, &used); err != nil {
		t.Fatalf("read token: %v", err)
	}
	if hash == res.CancelToken || hash != sha256Hex(res.CancelToken) {
		t.Errorf("stored token hash = %q, is not sha256(token)", hash)
	}
	if expires != wantPurge.Unix() {
		t.Errorf("token expires_at = %d, want %d", expires, wantPurge.Unix())
	}
	if used != nil {
		t.Errorf("token already used at %v", *used)
	}

	if n := env.count(`SELECT COUNT(*) FROM audit WHERE action = 'auth.deletion_request'`); n != 1 {
		t.Errorf("auth.deletion_request audit rows = %d, want 1", n)
	}

	// A second request while pending is a state error, not a new token.
	_, err = env.svc.RequestDeletion(ctx, p, testPassword)
	if apperr.CodeOf(err) != apperr.CodeInvalidState {
		t.Fatalf("repeat code = %v, want invalid_state (%v)", apperr.CodeOf(err), err)
	}
	if e, ok := apperr.From(err); !ok || e.Reason != apperr.ReasonAlreadyDeleted {
		t.Fatalf("repeat reason = %v, want already_deleted", err)
	}
}

func TestRequestDeletionRejectsWrongPassword(t *testing.T) {
	env := newAccountEnv(t)
	_, err := env.svc.RequestDeletion(context.Background(), alicePrincipal(env.alice), "not-the-password")
	if apperr.CodeOf(err) != apperr.CodeInvalidCredentials {
		t.Fatalf("code = %v, want invalid_credentials (%v)", apperr.CodeOf(err), err)
	}
	if n := env.count(`SELECT COUNT(*) FROM deletion_tokens`); n != 0 {
		t.Errorf("token rows = %d, want 0", n)
	}
}

func TestLoginAfterDeletionRequestIsForbidden(t *testing.T) {
	env := newAccountEnv(t)
	ctx := context.Background()
	if _, err := env.svc.RequestDeletion(ctx, alicePrincipal(env.alice), testPassword); err != nil {
		t.Fatalf("request deletion: %v", err)
	}
	_, err := env.ids.Login(ctx, idsvc.LoginRequest{Username: "alice", Password: testPassword, IP: "192.0.2.9"})
	if apperr.CodeOf(err) != apperr.CodeForbidden {
		t.Fatalf("login code = %v, want forbidden (%v)", apperr.CodeOf(err), err)
	}
	if e, ok := apperr.From(err); !ok || e.Reason != apperr.ReasonDeletionPending {
		t.Fatalf("login reason = %v, want deletion_pending", err)
	}
}

func TestCancelDeletionRestoresAccountOnce(t *testing.T) {
	env := newAccountEnv(t)
	ctx := context.Background()
	res, err := env.svc.RequestDeletion(ctx, alicePrincipal(env.alice), testPassword)
	if err != nil {
		t.Fatalf("request deletion: %v", err)
	}

	if err := env.svc.CancelDeletion(ctx, res.CancelToken); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	user, err := env.db.UserByID(ctx, env.alice)
	if err != nil {
		t.Fatalf("reload user: %v", err)
	}
	if user.Status != identity.StatusActive || user.DeletedAt != nil || user.PurgeAfter != nil {
		t.Fatalf("restored user = %+v, want active with no deletion marks", user)
	}
	var used *int64
	if err := env.db.QueryRowContext(ctx, `SELECT used_at FROM deletion_tokens WHERE user_id = ?`,
		env.alice).Scan(&used); err != nil {
		t.Fatalf("token: %v", err)
	}
	if used == nil {
		t.Fatal("cancel did not burn the token")
	}
	if n := env.count(`SELECT COUNT(*) FROM audit WHERE action = 'auth.deletion_cancel'`); n != 1 {
		t.Errorf("auth.deletion_cancel audit rows = %d, want 1", n)
	}

	// A one-time token cannot be replayed.
	if err := env.svc.CancelDeletion(ctx, res.CancelToken); apperr.CodeOf(err) != apperr.CodeBadRequest {
		t.Fatalf("replay code = %v, want bad_request (%v)", apperr.CodeOf(err), err)
	}
}

func TestCancelDeletionRejectsBadTokens(t *testing.T) {
	env := newAccountEnv(t)
	ctx := context.Background()
	res, err := env.svc.RequestDeletion(ctx, alicePrincipal(env.alice), testPassword)
	if err != nil {
		t.Fatalf("request deletion: %v", err)
	}

	for _, tc := range []struct {
		name  string
		token string
	}{
		{"empty", ""},
		{"unknown", strings.Repeat("ab", 32)},
	} {
		if err := env.svc.CancelDeletion(ctx, tc.token); apperr.CodeOf(err) != apperr.CodeBadRequest {
			t.Errorf("%s: code = %v, want bad_request (%v)", tc.name, apperr.CodeOf(err), err)
		} else if e, ok := apperr.From(err); !ok || e.Reason != apperr.ReasonInvalidValue {
			t.Errorf("%s: reason = %v, want invalid_value", tc.name, err)
		}
	}

	// Expired token: moving the clock past expires_at invalidates it.
	env.advance(GracePeriod + time.Minute)
	if err := env.svc.CancelDeletion(ctx, res.CancelToken); apperr.CodeOf(err) != apperr.CodeBadRequest {
		t.Fatalf("expired token code = %v, want bad_request (%v)", apperr.CodeOf(err), err)
	}
	if user, err := env.db.UserByID(ctx, env.alice); err != nil || user.Status != identity.StatusDisabled {
		t.Fatalf("expired token changed the account: %+v, %v", user, err)
	}
}

func TestExpiredDeletionIsPurgedWithTombstone(t *testing.T) {
	env := newAccountEnv(t)
	ctx := context.Background()
	if _, err := env.svc.RequestDeletion(ctx, alicePrincipal(env.alice), testPassword); err != nil {
		t.Fatalf("request deletion: %v", err)
	}
	// Before the window closes nothing may be destroyed.
	if n, tombstones, err := env.svc.PurgeExpired(ctx); err != nil || n != 0 || tombstones != 0 {
		t.Fatalf("early purge = %d/%d, %v; want 0/0", n, tombstones, err)
	}
	if user, err := env.db.UserByID(ctx, env.alice); err != nil || user.Status != identity.StatusDisabled {
		t.Fatalf("early purge touched the account: %+v, %v", user, err)
	}

	env.advance(GracePeriod + time.Minute)
	purged, _, err := env.svc.PurgeExpired(ctx)
	if err != nil {
		t.Fatalf("purge: %v", err)
	}
	if purged != 1 {
		t.Fatalf("purged = %d, want 1", purged)
	}
	if _, err := env.db.UserByID(ctx, env.alice); apperr.CodeOf(err) != apperr.CodeNotFound {
		t.Fatalf("purged user still readable: %v", err)
	}
	// FK cascades must have taken the owned rows with the account.
	for _, q := range []string{
		`SELECT COUNT(*) FROM nodes WHERE owner_id = ?`,
		`SELECT COUNT(*) FROM shares WHERE owner_id = ?`,
		`SELECT COUNT(*) FROM transfers WHERE owner_id = ?`,
		`SELECT COUNT(*) FROM sessions WHERE user_id = ?`,
		`SELECT COUNT(*) FROM deletion_tokens WHERE user_id = ?`,
	} {
		if n := env.count(q, env.alice); n != 0 {
			t.Errorf("leftover rows for %q: %d", q, n)
		}
	}
	if n := env.count(`SELECT COUNT(*) FROM audit WHERE action = 'system.account_purge'`); n != 1 {
		t.Errorf("system.account_purge audit rows = %d, want 1", n)
	}

	var expires int64
	if err := env.db.QueryRowContext(ctx,
		`SELECT expires_at FROM username_tombstones WHERE username_folded = 'alice'`).Scan(&expires); err != nil {
		t.Fatalf("tombstone: %v", err)
	}
	if want := env.now.Add(TombstoneRetention).Unix(); expires != want {
		t.Errorf("tombstone expires_at = %d, want %d", expires, want)
	}

	// The username is now reserved for registration and for the admin console.
	if ok, err := env.svc.TombstoneExists(ctx, "ALICE"); err != nil || !ok {
		t.Fatalf("TombstoneExists = %v, %v; want true", ok, err)
	}
	if _, err := env.svc.RequestDeletion(ctx, alicePrincipal(env.alice), testPassword); apperr.CodeOf(err) != apperr.CodeNotFound {
		t.Fatalf("deleted account still requestable: %v", err)
	}

	// Once the tombstone itself expires it is collected by the next pass.
	env.exec(`UPDATE username_tombstones SET expires_at = ? WHERE username_folded = 'alice'`, env.now.Add(-time.Hour).Unix())
	if _, tombstones, err := env.svc.PurgeExpired(ctx); err != nil || tombstones != 1 {
		t.Fatalf("tombstone cleanup = %d, %v; want 1", tombstones, err)
	}
}

func TestTombstoneBlocksRegistrationAndAdminCreate(t *testing.T) {
	env := newAccountEnv(t)
	ctx := context.Background()
	// Purge alice, which reserves the username for TombstoneRetention.
	if err := env.svc.PurgeUser(ctx, rootPrincipal(env.rootID), env.alice); err != nil {
		t.Fatalf("purge user: %v", err)
	}

	if err := env.db.SetSetting(ctx, sqlstore.SettingRegistrationOn, "1"); err != nil {
		t.Fatalf("enable registration: %v", err)
	}
	_, err := env.ids.Register(ctx, idsvc.RegisterRequest{
		Username: "Alice", Password: "brand-new-password-1", IP: "192.0.2.7",
	})
	if apperr.CodeOf(err) != apperr.CodeNameConflict {
		t.Fatalf("register code = %v, want conflict (%v)", apperr.CodeOf(err), err)
	}
	if e, ok := apperr.From(err); !ok || e.Reason != apperr.ReasonUsernameReserved {
		t.Fatalf("register reason = %v, want username_reserved", err)
	}

	p := rootPrincipal(env.rootID)
	if _, _, err := env.admin.CreateUser(ctx, p, adminsvc.CreateRequest{Username: "alice"}); apperr.CodeOf(err) != apperr.CodeNameConflict {
		t.Fatalf("admin create code = %v, want conflict (%v)", apperr.CodeOf(err), err)
	} else if e, ok := apperr.From(err); !ok || e.Reason != apperr.ReasonUsernameReserved {
		t.Fatalf("admin create reason = %v, want username_reserved", err)
	}
}

func TestAdminRestoreUser(t *testing.T) {
	env := newAccountEnv(t)
	ctx := context.Background()
	p := rootPrincipal(env.rootID)
	res, err := env.svc.RequestDeletion(ctx, alicePrincipal(env.alice), testPassword)
	if err != nil {
		t.Fatalf("request deletion: %v", err)
	}

	// Restoring an account that is not pending is a state conflict.
	if _, err := env.svc.RestoreUser(ctx, p, env.rootID+1000); apperr.CodeOf(err) != apperr.CodeNotFound {
		t.Fatalf("unknown target = %v, want not_found", err)
	}
	bob, err := env.db.UserByUsername(ctx, "bob")
	if err != nil {
		t.Fatalf("bob: %v", err)
	}
	if _, err := env.svc.RestoreUser(ctx, p, bob.ID); apperr.CodeOf(err) != apperr.CodeInvalidState {
		t.Fatalf("restore active user = %v, want invalid_state", err)
	}

	user, err := env.svc.RestoreUser(ctx, p, env.alice)
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	if user.Status != identity.StatusActive || user.DeletedAt != nil || user.PurgeAfter != nil {
		t.Fatalf("restored user = %+v, want active", user)
	}
	// The outstanding cancel token must be dead: an admin already undid it.
	if err := env.svc.CancelDeletion(ctx, res.CancelToken); apperr.CodeOf(err) != apperr.CodeBadRequest {
		t.Fatalf("cancel after restore = %v, want bad_request", err)
	}
	if n := env.count(`SELECT COUNT(*) FROM audit WHERE action = 'admin.user_restore'`); n != 1 {
		t.Errorf("admin.user_restore audit rows = %d, want 1", n)
	}
}

func TestAdminPurgeUserGuards(t *testing.T) {
	env := newAccountEnv(t)
	ctx := context.Background()
	p := rootPrincipal(env.rootID)

	// Deleting yourself is refused.
	if err := env.svc.PurgeUser(ctx, p, env.rootID); apperr.CodeOf(err) != apperr.CodeInvalidState {
		t.Fatalf("self purge = %v, want invalid_state", err)
	} else if e, ok := apperr.From(err); !ok || e.Reason != apperr.ReasonSelfTarget {
		t.Fatalf("self purge reason = %v, want self_target", err)
	}

	// The root administrator is protected even when targeted by another admin.
	second, err := env.db.CreateUser(ctx, identity.User{
		Username: "admin2", UsernameFolded: "admin2", DisplayName: "admin2",
		PasswordHash: adminHashForTest(t), RoleID: mustRole(t, env.db, adminsvc.RoleAdmin),
		Status: identity.StatusActive,
	})
	if err != nil {
		t.Fatalf("create admin2: %v", err)
	}
	p2 := access.Principal{UserID: second.ID, Username: "admin2", Role: adminsvc.RoleAdmin,
		Perms: access.NewPermSet(access.ActionAdminUsers)}
	if err := env.svc.PurgeUser(ctx, p2, env.rootID); apperr.CodeOf(err) != apperr.CodeInvalidState {
		t.Fatalf("root purge = %v, want invalid_state", err)
	} else if e, ok := apperr.From(err); !ok || e.Reason != apperr.ReasonRootAdmin {
		t.Fatalf("root purge reason = %v, want root_admin", err)
	}

	// admin2 is the last active administrator once root is disabled.
	env.exec(`UPDATE users SET status = 'disabled' WHERE id = ?`, env.rootID)
	if err := env.svc.PurgeUser(ctx, p, second.ID); apperr.CodeOf(err) != apperr.CodeInvalidState {
		t.Fatalf("last admin purge = %v, want invalid_state", err)
	} else if e, ok := apperr.From(err); !ok || e.Reason != apperr.ReasonLastAdmin {
		t.Fatalf("last admin reason = %v, want last_admin", err)
	}
	env.exec(`UPDATE users SET status = 'active' WHERE id = ?`, env.rootID)

	// A regular account can be purged immediately, writing its tombstone.
	if err := env.svc.PurgeUser(ctx, p, env.alice); err != nil {
		t.Fatalf("purge alice: %v", err)
	}
	if _, err := env.db.UserByID(ctx, env.alice); apperr.CodeOf(err) != apperr.CodeNotFound {
		t.Fatalf("purged user still readable: %v", err)
	}
	if n := env.count(`SELECT COUNT(*) FROM username_tombstones WHERE username_folded = 'alice'`); n != 1 {
		t.Errorf("tombstones = %d, want 1", n)
	}
	if n := env.count(`SELECT COUNT(*) FROM audit WHERE action = 'admin.user_purge'`); n != 1 {
		t.Errorf("admin.user_purge audit rows = %d, want 1", n)
	}
}

func TestAdminPurgeUserSucceedsForAdminPeer(t *testing.T) {
	env := newAccountEnv(t)
	ctx := context.Background()
	roleID := mustRole(t, env.db, adminsvc.RoleAdmin)
	peer, err := env.db.CreateUser(ctx, identity.User{
		Username: "admin3", UsernameFolded: "admin3", DisplayName: "admin3",
		PasswordHash: adminHashForTest(t), RoleID: roleID, Status: identity.StatusActive,
	})
	if err != nil {
		t.Fatalf("create admin3: %v", err)
	}
	// Two active administrators remain (root + admin3), so the guard passes.
	if err := env.svc.PurgeUser(ctx, rootPrincipal(env.rootID), peer.ID); err != nil {
		t.Fatalf("purge admin peer: %v", err)
	}
	if _, err := env.db.UserByID(ctx, peer.ID); apperr.CodeOf(err) != apperr.CodeNotFound {
		t.Fatalf("peer still readable: %v", err)
	}
}

func adminHashForTest(t *testing.T) string {
	t.Helper()
	h, err := passwd.Hash(adminPassword)
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	return h
}

func mustRole(t *testing.T, db *sqlstore.DB, code string) int64 {
	t.Helper()
	id, err := db.RoleIDByCode(context.Background(), code)
	if err != nil {
		t.Fatalf("role %q: %v", code, err)
	}
	return id
}

func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}
