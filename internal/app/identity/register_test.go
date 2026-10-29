package identity

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	domainidentity "rapig/internal/domain/identity"
	sqlstore "rapig/internal/infra/sql"
	"rapig/internal/platform/apperr"
	"rapig/migrations"
)

func newRegisterEnv(t *testing.T) (*sqlstore.DB, *Service) {
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
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	return db, NewService(db, log)
}

func enableRegistration(t *testing.T, db *sqlstore.DB) {
	t.Helper()
	if err := db.SetSetting(context.Background(), sqlstore.SettingRegistrationOn, "1"); err != nil {
		t.Fatalf("enable registration: %v", err)
	}
}

func TestRegistrationEnabledDefaultsToFalse(t *testing.T) {
	_, svc := newRegisterEnv(t)
	enabled, err := svc.RegistrationEnabled(context.Background())
	if err != nil {
		t.Fatalf("registration enabled: %v", err)
	}
	if enabled {
		t.Fatal("registration is open by default")
	}
}

func TestRegisterRejectedWhenDisabled(t *testing.T) {
	_, svc := newRegisterEnv(t)
	_, err := svc.Register(context.Background(), RegisterRequest{
		Username: "carol", Password: "carol-password-1", IP: "192.0.2.1",
	})
	if apperr.CodeOf(err) != apperr.CodeForbidden {
		t.Fatalf("code = %v, want forbidden (%v)", apperr.CodeOf(err), err)
	}
	if e, ok := apperr.From(err); !ok || e.Reason != apperr.ReasonRegistrationDisabled {
		t.Fatalf("reason = %v, want registration_disabled", err)
	}
}

func TestRegisterCreatesAccount(t *testing.T) {
	db, svc := newRegisterEnv(t)
	enableRegistration(t, db)
	ctx := context.Background()

	user, err := svc.Register(ctx, RegisterRequest{
		Username:  "Carol",
		Email:     "Carol@Example.com",
		Phone:     "13800138000",
		Password:  "carol-password-1",
		IP:        "192.0.2.1",
		UserAgent: "go-test",
		RequestID: "req-1",
	})
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	if user.Username != "Carol" || user.UsernameFolded != "carol" {
		t.Fatalf("username not stored: %+v", user)
	}
	if user.Email != "Carol@Example.com" || user.EmailFolded != "carol@example.com" {
		t.Fatalf("email not stored: %+v", user)
	}
	if user.Phone != "13800138000" || user.PhoneFolded != "13800138000" {
		t.Fatalf("phone not stored: %+v", user)
	}
	if user.RoleCode != "user" || user.Status != domainidentity.StatusActive {
		t.Fatalf("unexpected role or status: %+v", user)
	}
	if user.QuotaBytes != DefaultRegistrationQuotaBytes {
		t.Fatalf("quota = %d, want %d", user.QuotaBytes, DefaultRegistrationQuotaBytes)
	}
	if user.IsRoot {
		t.Fatal("a registered account must not be root")
	}
	// The account can log in with any of the three identifiers afterwards.
	for _, identifier := range []string{"carol", "CAROL@example.com", "13800138000"} {
		if _, err := svc.Login(ctx, LoginRequest{Username: identifier, Password: "carol-password-1"}); err != nil {
			t.Fatalf("login with %q: %v", identifier, err)
		}
	}
	// Registration is audited as the new account, not as an administrator.
	page, err := db.ListAudit(ctx, sqlstore.AuditFilter{Limit: 20})
	if err != nil {
		t.Fatalf("list audit: %v", err)
	}
	found := false
	for _, ev := range page.Items {
		if ev.Action == "auth.register" && ev.Result == "success" {
			found = true
			if ev.ActorID != user.ID {
				t.Fatalf("audit actor = %d, want %d", ev.ActorID, user.ID)
			}
			if ev.TargetType != "user" || ev.TargetID != user.ID {
				t.Fatalf("audit target = %s/%d, want user/%d", ev.TargetType, ev.TargetID, user.ID)
			}
		}
	}
	if !found {
		t.Fatal("registration was not audited")
	}
}

func TestRegisterGeneratesUsername(t *testing.T) {
	db, svc := newRegisterEnv(t)
	enableRegistration(t, db)
	ctx := context.Background()

	user, err := svc.Register(ctx, RegisterRequest{
		Email: "dana@example.com", Password: "dana-password-1", IP: "192.0.2.2",
	})
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	if !strings.HasPrefix(user.Username, "u") || len(user.Username) != 9 {
		t.Fatalf("generated username = %q, want u + 8 characters", user.Username)
	}
	for _, r := range strings.TrimPrefix(user.Username, "u") {
		if !(r >= 'a' && r <= 'z') && !(r >= '0' && r <= '9') {
			t.Fatalf("generated username %q is not lowercase alphanumeric", user.Username)
		}
	}
	if _, err := svc.Login(ctx, LoginRequest{Username: "dana@example.com", Password: "dana-password-1"}); err != nil {
		t.Fatalf("login with email: %v", err)
	}

	phoneUser, err := svc.Register(ctx, RegisterRequest{
		Phone: "+86 139-0013-9000", Password: "phone-password-1", IP: "192.0.2.3",
	})
	if err != nil {
		t.Fatalf("register by phone: %v", err)
	}
	if phoneUser.PhoneFolded != "+8613900139000" {
		t.Fatalf("phone not normalized: %+v", phoneUser)
	}
	if _, err := svc.Login(ctx, LoginRequest{Username: "+8613900139000", Password: "phone-password-1"}); err != nil {
		t.Fatalf("login with phone: %v", err)
	}
}

func TestRegisterDuplicateIdentifier(t *testing.T) {
	db, svc := newRegisterEnv(t)
	enableRegistration(t, db)
	ctx := context.Background()

	if _, err := svc.Register(ctx, RegisterRequest{
		Username: "erin", Email: "erin@example.com", Phone: "13700137000",
		Password: "erin-password-1", IP: "192.0.2.4",
	}); err != nil {
		t.Fatalf("register: %v", err)
	}
	cases := []RegisterRequest{
		{Username: "ERIN", Password: "other-password-1", IP: "192.0.2.5"},
		{Email: "ERIN@example.com", Password: "other-password-1", IP: "192.0.2.6"},
		{Phone: "13700137000", Password: "other-password-1", IP: "192.0.2.7"},
	}
	for _, req := range cases {
		_, err := svc.Register(ctx, req)
		if apperr.CodeOf(err) != apperr.CodeNameConflict {
			t.Fatalf("register %+v: code = %v, want name_conflict", req, apperr.CodeOf(err))
		}
		if e, ok := apperr.From(err); !ok || e.Reason != apperr.ReasonIdentifierTaken {
			t.Fatalf("register %+v: reason = %v, want identifier_taken", req, err)
		}
	}

	// Cross namespace: a username that is purely numeric can later collide with
	// somebody else's phone number.
	if _, err := svc.Register(ctx, RegisterRequest{
		Username: "13700137001", Password: "numeric-password-1", IP: "192.0.2.9",
	}); err != nil {
		t.Fatalf("register numeric username: %v", err)
	}
	_, err := svc.Register(ctx, RegisterRequest{
		Phone: "13700137001", Password: "other-password-1", IP: "192.0.2.10",
	})
	if apperr.CodeOf(err) != apperr.CodeNameConflict {
		t.Fatalf("cross-namespace phone: code = %v, want name_conflict (%v)", apperr.CodeOf(err), err)
	}
	if e, ok := apperr.From(err); !ok || e.Reason != apperr.ReasonIdentifierTaken {
		t.Fatalf("cross-namespace phone: reason = %v, want identifier_taken", err)
	}
}

func TestRegisterValidation(t *testing.T) {
	db, svc := newRegisterEnv(t)
	enableRegistration(t, db)
	ctx := context.Background()

	cases := []struct {
		name string
		req  RegisterRequest
		code apperr.Code
		reas string
	}{
		{"no identifier", RegisterRequest{Password: "valid-password-1"}, apperr.CodeBadRequest, apperr.ReasonInvalidValue},
		{"bad email", RegisterRequest{Email: "not-an-email", Password: "valid-password-1"}, apperr.CodeBadRequest, apperr.ReasonInvalidValue},
		{"bad phone", RegisterRequest{Phone: "12ab", Password: "valid-password-1"}, apperr.CodeBadRequest, apperr.ReasonInvalidValue},
		{"bad username", RegisterRequest{Username: "a", Password: "valid-password-1"}, apperr.CodeInvalidName, apperr.ReasonInvalidValue},
		{"short password", RegisterRequest{Username: "frank", Password: "short"}, apperr.CodeBadRequest, apperr.ReasonPasswordTooShort},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tc.req.IP = "198.51.100.1"
			_, err := svc.Register(ctx, tc.req)
			if apperr.CodeOf(err) != tc.code {
				t.Fatalf("code = %v, want %v (%v)", apperr.CodeOf(err), tc.code, err)
			}
			if e, ok := apperr.From(err); !ok || e.Reason != tc.reas {
				t.Fatalf("reason = %v, want %s", err, tc.reas)
			}
		})
	}
}

func TestRegisterRateLimit(t *testing.T) {
	db, svc := newRegisterEnv(t)
	enableRegistration(t, db)
	ctx := context.Background()
	fixed := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	svc.now = func() time.Time { return fixed }

	for i := 0; i < RegistrationIPLimit; i++ {
		username := "limit" + string(rune('a'+i))
		if _, err := svc.Register(ctx, RegisterRequest{
			Username: username, Password: "limit-password-1", IP: "203.0.113.9",
		}); err != nil {
			t.Fatalf("register %d: %v", i, err)
		}
	}
	_, err := svc.Register(ctx, RegisterRequest{
		Username: "limit-over", Password: "limit-password-1", IP: "203.0.113.9",
	})
	if apperr.CodeOf(err) != apperr.CodeTooManyAttempts {
		t.Fatalf("code = %v, want too_many_attempts (%v)", apperr.CodeOf(err), err)
	}
	e, ok := apperr.From(err)
	if !ok || e.Reason != apperr.ReasonRateLimited {
		t.Fatalf("reason = %v, want rate_limited", err)
	}
	if e.RetryAfter <= 0 || e.RetryAfter > int(RegistrationWindow.Seconds())+1 {
		t.Fatalf("retry after = %d, want 1..%v", e.RetryAfter, RegistrationWindow)
	}
	// Another IP is unaffected.
	if _, err := svc.Register(ctx, RegisterRequest{
		Username: "limit-other", Password: "limit-password-1", IP: "203.0.113.10",
	}); err != nil {
		t.Fatalf("register from other ip: %v", err)
	}
	// After the window slides past, the same IP may register again.
	svc.now = func() time.Time { return fixed.Add(RegistrationWindow + time.Minute) }
	if _, err := svc.Register(ctx, RegisterRequest{
		Username: "limit-later", Password: "limit-password-1", IP: "203.0.113.9",
	}); err != nil {
		t.Fatalf("register after window: %v", err)
	}
}

func TestRegisterRateLimiterSweeps(t *testing.T) {
	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	limiter := newRegisterLimiter(func() time.Time { return now })
	for i := 0; i < RegistrationIPLimit; i++ {
		if ok, _ := limiter.allow("203.0.113.20"); !ok {
			t.Fatalf("hit %d denied", i)
		}
	}
	if ok, retry := limiter.allow("203.0.113.20"); ok || retry <= 0 {
		t.Fatalf("sixth hit allowed=%v retry=%d", ok, retry)
	}
	now = now.Add(RegistrationWindow + time.Second)
	if ok, _ := limiter.allow("203.0.113.20"); !ok {
		t.Fatal("hit after the window denied")
	}
	// A second full window empties the map.
	now = now.Add(2 * RegistrationWindow)
	limiter.allow("203.0.113.21")
	limiter.mu.Lock()
	remaining := len(limiter.hits)
	limiter.mu.Unlock()
	if remaining != 1 {
		t.Fatalf("limiter kept %d ips, want 1", remaining)
	}
}
