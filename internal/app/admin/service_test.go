package admin

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"

	"rapig/internal/domain/access"
	"rapig/internal/domain/identity"
	"rapig/internal/infra/sql"
	"rapig/internal/platform/apperr"
	"rapig/internal/platform/passwd"
	"rapig/migrations"
)

func newTestEnv(t *testing.T) (*sqlstore.DB, *Service, int64) {
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
	roleID, err := db.RoleIDByCode(ctx, RoleAdmin)
	if err != nil {
		t.Fatalf("role: %v", err)
	}
	hash, err := passwd.Hash("admin-password-1")
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	admin, err := db.CreateUser(ctx, identity.User{
		Username: "admin", UsernameFolded: "admin", DisplayName: "admin",
		PasswordHash: hash, RoleID: roleID, Status: identity.StatusActive,
		// The fixture administrator is the root account, like the bootstrap one:
		// only root may toggle self registration.
		IsRoot: true,
	})
	if err != nil {
		t.Fatalf("create admin: %v", err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	return db, New(db, log), admin.ID
}

func adminPrincipal(id int64) access.Principal {
	return access.Principal{
		UserID: id, Username: "admin", Role: RoleAdmin,
		Perms: access.NewPermSet(access.ActionAdminUsers, access.ActionAdminSettings, access.ActionAdminAudit),
	}
}

func TestCreateUserAndGeneratedPassword(t *testing.T) {
	_, svc, adminID := newTestEnv(t)
	ctx := context.Background()
	p := adminPrincipal(adminID)

	u, password, err := svc.CreateUser(ctx, p, CreateRequest{Username: "carol"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if u.Status != identity.StatusActive || u.RoleCode != RoleUser {
		t.Fatalf("unexpected user: %+v", u)
	}
	if len(password) < 16 {
		t.Fatalf("generated password too short: %q", password)
	}
	if err := passwd.Verify(u.PasswordHash, password); err != nil {
		t.Fatalf("generated password does not verify: %v", err)
	}
	if _, _, err := svc.CreateUser(ctx, p, CreateRequest{Username: "CAROL"}); err == nil {
		t.Fatal("duplicate username accepted")
	}
	for _, bad := range []CreateRequest{
		{Username: "a"},
		{Username: "bad name"},
		{Username: "dave", Password: "short"},
		{Username: "dave", Role: "root"},
		{Username: "dave", QuotaBytes: -1},
	} {
		if _, _, err := svc.CreateUser(ctx, p, bad); err == nil {
			t.Fatalf("invalid create accepted: %+v", bad)
		}
	}
}

func TestUpdateGuardsAndLastAdmin(t *testing.T) {
	db, svc, adminID := newTestEnv(t)
	ctx := context.Background()
	p := adminPrincipal(adminID)
	disabled := identity.StatusDisabled
	roleUser := RoleUser

	if _, err := svc.UpdateUser(ctx, p, adminID, UpdateRequest{Status: &disabled}); err == nil {
		t.Fatal("self-disable allowed")
	} else if apperrReason(err) != "self_target" {
		t.Fatalf("want self_target, got %v", err)
	}
	if _, err := svc.UpdateUser(ctx, p, adminID, UpdateRequest{Role: &roleUser}); err == nil {
		t.Fatal("self-demote allowed")
	}
	if _, err := svc.UpdateUser(ctx, p, 999999, UpdateRequest{Status: &disabled}); err == nil {
		t.Fatal("missing user accepted")
	}

	// A principal that is not the sole admin in the database still cannot
	// remove the last usable administrator.
	other := access.Principal{UserID: 424242, Username: "other", Role: RoleAdmin}
	if _, err := svc.UpdateUser(ctx, other, adminID, UpdateRequest{Status: &disabled}); err == nil {
		t.Fatal("removing the last admin allowed")
	} else if apperrReason(err) != "last_admin" {
		t.Fatalf("want last_admin, got %v", err)
	}

	u, _, err := svc.CreateUser(ctx, p, CreateRequest{Username: "bob", Role: RoleAdmin})
	if err != nil {
		t.Fatalf("create bob: %v", err)
	}
	quota := int64(1000)
	updated, err := svc.UpdateUser(ctx, p, u.ID, UpdateRequest{QuotaBytes: &quota})
	if err != nil {
		t.Fatalf("update quota: %v", err)
	}
	if updated.QuotaBytes != 1000 {
		t.Fatalf("quota not applied: %+v", updated)
	}
	if _, err := db.CountActiveAdmins(ctx); err != nil {
		t.Fatalf("count admins: %v", err)
	}
}

func TestResetPasswordRevokesSessions(t *testing.T) {
	db, svc, adminID := newTestEnv(t)
	ctx := context.Background()
	p := adminPrincipal(adminID)
	u, _, err := svc.CreateUser(ctx, p, CreateRequest{Username: "erin", Password: "erin-password-1"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := db.CreateSession(ctx, identity.Session{ID: "session-hash", UserID: u.ID, CSRFHash: "x"}); err != nil {
		t.Fatalf("session: %v", err)
	}
	if err != nil {
		t.Fatalf("session: %v", err)
	}
	password, err := svc.ResetPassword(ctx, p, u.ID)
	if err != nil {
		t.Fatalf("reset: %v", err)
	}
	if _, err := db.UserByID(ctx, u.ID); err != nil {
		t.Fatalf("user: %v", err)
	}
	if _, _, err := db.SessionByID(ctx, "session-hash"); err == nil {
		t.Fatal("session survived password reset")
	}
	if _, err := svc.ResetPassword(ctx, p, adminID); err == nil {
		t.Fatal("self reset allowed")
	}
	updated, _ := db.UserByID(ctx, u.ID)
	if err := passwd.Verify(updated.PasswordHash, password); err != nil {
		t.Fatalf("new password does not verify: %v", err)
	}
}

func TestAuditAndSettings(t *testing.T) {
	db, svc, adminID := newTestEnv(t)
	ctx := context.Background()
	p := adminPrincipal(adminID)
	if _, _, err := svc.CreateUser(ctx, p, CreateRequest{Username: "frank"}); err != nil {
		t.Fatalf("create: %v", err)
	}
	page, err := db.ListAudit(ctx, sqlstore.AuditFilter{Limit: 20})
	if err != nil {
		t.Fatalf("audit: %v", err)
	}
	if page.Total == 0 {
		t.Fatal("expected audit rows")
	}
	actions, err := db.AuditActions(ctx)
	if err != nil || len(actions) == 0 {
		t.Fatalf("actions: %v %v", actions, err)
	}
	filtered, err := db.ListAudit(ctx, sqlstore.AuditFilter{Action: "admin.user_create", Limit: 10})
	if err != nil {
		t.Fatalf("filtered audit: %v", err)
	}
	for _, ev := range filtered.Items {
		if ev.Action != "admin.user_create" {
			t.Fatalf("filter leaked action %q", ev.Action)
		}
	}

	got, err := svc.GetSettings(ctx, p)
	if err != nil {
		t.Fatalf("settings: %v", err)
	}
	if got.SiteName != sqlstore.DefaultSiteName || got.RegistrationEnabled {
		t.Fatalf("unexpected defaults: %+v", got)
	}
	name := "我的网盘"
	enabled := true
	quota := int64(1_000_000_000)
	upd, err := svc.UpdateSettings(ctx, p, SettingsPatch{SiteName: &name, RegistrationEnabled: &enabled, DefaultQuotaBytes: &quota})
	if err != nil {
		t.Fatalf("update settings: %v", err)
	}
	if upd.SiteName != name || !upd.RegistrationEnabled || upd.DefaultQuotaBytes != quota {
		t.Fatalf("settings not applied: %+v", upd)
	}
	long := strings.Repeat("x", MaxSiteNameLen+1)
	if _, err := svc.UpdateSettings(ctx, p, SettingsPatch{SiteName: &long}); err == nil {
		t.Fatal("long site name accepted")
	}
	neg := int64(-5)
	if _, err := svc.UpdateSettings(ctx, p, SettingsPatch{DefaultQuotaBytes: &neg}); err == nil {
		t.Fatal("negative quota accepted")
	}
}

func apperrReason(err error) string {
	if e, ok := apperr.From(err); ok {
		return e.Reason
	}
	return ""
}
