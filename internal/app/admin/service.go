// Package admin implements the M5.2 administration service: accounts, audit
// browsing and site settings. Permission checks happen in the HTTP layer
// (authz.Require); the guards here protect against self-lockout and validate
// admin supplied values.
package admin

import (
	"context"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"rapig/internal/domain/access"
	"rapig/internal/domain/identity"
	"rapig/internal/infra/sql"
	"rapig/internal/platform/apperr"
	"rapig/internal/platform/idgen"
	"rapig/internal/platform/passwd"
)

const (
	MinUsernameLen = 2
	MaxUsernameLen = 32
	MinPasswordLen = 8
	MaxPasswordLen = 128
	MaxSiteNameLen = 64
	RoleUser       = "user"
	RoleAdmin      = "admin"
)

// Service holds the admin use cases.
type Service struct {
	db  *sqlstore.DB
	log *slog.Logger
	now func() time.Time
}

// New builds the admin service.
func New(db *sqlstore.DB, log *slog.Logger) *Service {
	return &Service{db: db, log: log, now: time.Now}
}

// UserPage is one page of accounts.
type UserPage struct {
	Items []identity.User
	Total int
}

// CreateRequest is the POST /admin/users payload. Email and phone are optional
// identifiers; empty means unset.
type CreateRequest struct {
	Username   string
	Email      string
	Phone      string
	Password   string
	Role       string
	QuotaBytes int64
}

// UpdateRequest is the PATCH /admin/users/{id} payload; nil fields are kept.
type UpdateRequest struct {
	Status     *string
	QuotaBytes *int64
	Role       *string
	// CanCreateGroups 是 P1 的建组授权，只有 admin.users 能改。
	CanCreateGroups *bool
}

// ListUsers pages through accounts, newest filterable by q.
func (s *Service) ListUsers(ctx context.Context, p access.Principal, q string, limit, offset int) (UserPage, error) {
	items, total, err := s.db.ListUsers(ctx, q, limit, offset)
	if err != nil {
		return UserPage{}, err
	}
	return UserPage{Items: items, Total: total}, nil
}

// CreateUser adds an account. A generated password is returned when the caller
// left it empty; it is shown once by the HTTP layer. The optional e-mail and
// phone number follow the same rules as self registration.
func (s *Service) CreateUser(ctx context.Context, p access.Principal, req CreateRequest) (identity.User, string, error) {
	username := strings.TrimSpace(req.Username)
	if !validUsername(username) {
		return identity.User{}, "", apperr.ErrInvalidName.WithReason(apperr.ReasonInvalidValue)
	}
	email, emailFolded, ok := identity.NormalizeEmail(req.Email)
	if !ok {
		return identity.User{}, "", apperr.New(apperr.CodeBadRequest, http.StatusBadRequest,
			"邮箱格式不正确").WithReason(apperr.ReasonInvalidValue)
	}
	phone, phoneFolded, ok := identity.NormalizePhone(req.Phone)
	if !ok {
		return identity.User{}, "", apperr.New(apperr.CodeBadRequest, http.StatusBadRequest,
			"手机号格式不正确（6–20 位数字，可带 + 前缀）").WithReason(apperr.ReasonInvalidValue)
	}
	password := req.Password
	if password == "" {
		password = idgen.MustToken(15)
	} else if !validPassword(password) {
		return identity.User{}, "", apperr.ErrInvalidName.WithReason(apperr.ReasonPasswordTooShort)
	}
	roleCode := req.Role
	if roleCode == "" {
		roleCode = RoleUser
	}
	if roleCode != RoleUser && roleCode != RoleAdmin {
		return identity.User{}, "", apperr.ErrBadRequest.WithReason(apperr.ReasonInvalidRole)
	}
	if req.QuotaBytes < 0 {
		return identity.User{}, "", apperr.ErrBadRequest.WithReason(apperr.ReasonInvalidValue)
	}
	// Username, e-mail and phone share one namespace: any clash is a conflict.
	// A name reserved by a deleted account is also off limits for the tombstone
	// window, otherwise the new account would inherit the old one's identity.
	reserved, err := s.db.UsernameTombstoneExists(ctx, identity.FoldUsername(username), s.now().UTC())
	if err != nil {
		return identity.User{}, "", err
	}
	if reserved {
		return identity.User{}, "", apperr.ErrNameConflict.WithReason(apperr.ReasonUsernameReserved)
	}
	taken, err := s.db.IdentifierTaken(ctx, identity.FoldUsername(username), emailFolded, phoneFolded)
	if err != nil {
		return identity.User{}, "", err
	}
	if taken {
		return identity.User{}, "", apperr.ErrNameConflict.WithReason(apperr.ReasonIdentifierTaken)
	}
	roleID, err := s.db.RoleIDByCode(ctx, roleCode)
	if err != nil {
		return identity.User{}, "", err
	}
	hash, err := passwd.Hash(password)
	if err != nil {
		return identity.User{}, "", err
	}
	u, err := s.db.CreateUser(ctx, identity.User{
		Username:       username,
		UsernameFolded: identity.FoldUsername(username),
		DisplayName:    username,
		PasswordHash:   hash,
		RoleID:         roleID,
		Status:         identity.StatusActive,
		QuotaBytes:     req.QuotaBytes,
		Email:          email,
		EmailFolded:    emailFolded,
		Phone:          phone,
		PhoneFolded:    phoneFolded,
	})
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique constraint") {
			return identity.User{}, "", apperr.ErrNameConflict.WithReason(apperr.ReasonIdentifierTaken)
		}
		return identity.User{}, "", err
	}
	detail := "username=" + u.Username
	if u.Email != "" {
		detail += " email=" + u.Email
	}
	if u.Phone != "" {
		detail += " phone=" + u.Phone
	}
	s.audit(ctx, p, "admin.user_create", "user", u.ID, detail)
	return u, password, nil
}

// UpdateUser changes status, quota or role, with self-lockout guards.
func (s *Service) UpdateUser(ctx context.Context, p access.Principal, targetID int64, req UpdateRequest) (identity.User, error) {
	target, err := s.db.UserByID(ctx, targetID)
	if err != nil {
		return identity.User{}, err
	}
	if req.Status != nil {
		if *req.Status != identity.StatusActive && *req.Status != identity.StatusDisabled {
			return identity.User{}, apperr.ErrBadRequest.WithReason(apperr.ReasonInvalidValue)
		}
	}
	var roleID *int64
	if req.Role != nil {
		if *req.Role != RoleUser && *req.Role != RoleAdmin {
			return identity.User{}, apperr.ErrBadRequest.WithReason(apperr.ReasonInvalidRole)
		}
		id, err := s.db.RoleIDByCode(ctx, *req.Role)
		if err != nil {
			return identity.User{}, err
		}
		roleID = &id
	}
	if req.QuotaBytes != nil && *req.QuotaBytes < 0 {
		return identity.User{}, apperr.ErrBadRequest.WithReason(apperr.ReasonInvalidValue)
	}
	disablingSelf := target.ID == p.UserID && req.Status != nil && *req.Status != identity.StatusActive
	demotingSelf := target.ID == p.UserID && req.Role != nil && *req.Role != RoleAdmin
	if disablingSelf || demotingSelf {
		return identity.User{}, apperr.ErrInvalidState.WithReason(apperr.ReasonSelfTarget)
	}
	// Refuse to remove the last usable administrator.
	losesAdmin := target.RoleCode == RoleAdmin && target.Status == identity.StatusActive &&
		((req.Status != nil && *req.Status != identity.StatusActive) ||
			(req.Role != nil && *req.Role != RoleAdmin))
	if losesAdmin {
		n, err := s.db.CountActiveAdmins(ctx)
		if err != nil {
			return identity.User{}, err
		}
		if n <= 1 {
			return identity.User{}, apperr.ErrInvalidState.WithReason(apperr.ReasonLastAdmin)
		}
	}
	// The root administrator cannot be disabled or demoted: it is the only
	// account allowed to toggle self registration.
	if target.IsRoot {
		if req.Status != nil && *req.Status != identity.StatusActive {
			return identity.User{}, apperr.ErrInvalidState.WithReason(apperr.ReasonRootAdmin)
		}
		if req.Role != nil && *req.Role != RoleAdmin {
			return identity.User{}, apperr.ErrInvalidState.WithReason(apperr.ReasonRootAdmin)
		}
	}
	if err := s.db.UpdateUser(ctx, targetID, req.Status, req.QuotaBytes, roleID, req.CanCreateGroups); err != nil {
		return identity.User{}, err
	}
	updated, err := s.db.UserByID(ctx, targetID)
	if err != nil {
		return identity.User{}, err
	}
	s.audit(ctx, p, "admin.user_update", "user", targetID, "username="+target.Username)
	return updated, nil
}

// ResetPassword sets a new one-time password and revokes existing sessions.
func (s *Service) ResetPassword(ctx context.Context, p access.Principal, targetID int64) (string, error) {
	target, err := s.db.UserByID(ctx, targetID)
	if err != nil {
		return "", err
	}
	if target.ID == p.UserID {
		return "", apperr.ErrInvalidState.WithReason(apperr.ReasonSelfTarget)
	}
	password := idgen.MustToken(15)
	hash, err := passwd.Hash(password)
	if err != nil {
		return "", err
	}
	if err := s.db.ResetUserPassword(ctx, targetID, hash, s.now().UTC()); err != nil {
		return "", err
	}
	s.audit(ctx, p, "admin.password_reset", "user", targetID, "username="+target.Username)
	return password, nil
}

// ListAudit pages through the audit log.
func (s *Service) ListAudit(ctx context.Context, p access.Principal, f sqlstore.AuditFilter) (sqlstore.AuditPage, error) {
	return s.db.ListAudit(ctx, f)
}

// AuditActions returns the distinct action codes for filtering.
func (s *Service) AuditActions(ctx context.Context, p access.Principal) ([]string, error) {
	return s.db.AuditActions(ctx)
}

// Settings is the site settings view returned by the admin API.
type Settings struct {
	SiteName            string
	RegistrationEnabled bool
	DeletionEnabled     bool
	DefaultQuotaBytes   int64
	AuditRetentionDays  int
	TrashRetentionDays  int
}

// SettingsPatch is the PUT /admin/settings payload; nil fields are kept.
type SettingsPatch struct {
	SiteName            *string
	RegistrationEnabled *bool
	DeletionEnabled     *bool
	DefaultQuotaBytes   *int64
}

// GetSettings reads the settings with documented defaults.
func (s *Service) GetSettings(ctx context.Context, p access.Principal) (Settings, error) {
	out := Settings{
		SiteName:           sqlstore.DefaultSiteName,
		AuditRetentionDays: sqlstore.DefaultAuditRetentionDays,
		TrashRetentionDays: sqlstore.DefaultTrashRetentionDays,
	}
	if v, ok, err := s.db.Setting(ctx, sqlstore.SettingSiteName); err != nil {
		return Settings{}, err
	} else if ok && strings.TrimSpace(v) != "" {
		out.SiteName = v
	}
	if v, ok, err := s.db.Setting(ctx, sqlstore.SettingRegistrationOn); err != nil {
		return Settings{}, err
	} else if ok {
		out.RegistrationEnabled = v == "1" || strings.EqualFold(v, "true")
	}
	if v, ok, err := s.db.Setting(ctx, sqlstore.SettingDeletionEnabled); err != nil {
		return Settings{}, err
	} else if ok {
		out.DeletionEnabled = v == "1" || strings.EqualFold(v, "true")
	}
	if v, ok, err := s.db.Setting(ctx, sqlstore.SettingDefaultQuotaBytes); err != nil {
		return Settings{}, err
	} else if ok {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n >= 0 {
			out.DefaultQuotaBytes = n
		}
	}
	if v, ok, err := s.db.Setting(ctx, sqlstore.SettingAuditRetentionDays); err != nil {
		return Settings{}, err
	} else if ok {
		if n, err := strconv.Atoi(v); err == nil && n >= 1 && n <= 3650 {
			out.AuditRetentionDays = n
		}
	}
	if v, ok, err := s.db.Setting(ctx, sqlstore.SettingTrashRetentionDays); err != nil {
		return Settings{}, err
	} else if ok {
		if n, err := strconv.Atoi(v); err == nil && n >= 1 && n <= 365 {
			out.TrashRetentionDays = n
		}
	}
	return out, nil
}

// UpdateSettings validates and persists the writable settings. Only the root
// administrator may open or close self registration; every other key stays
// available to any administrator.
func (s *Service) UpdateSettings(ctx context.Context, p access.Principal, patch SettingsPatch) (Settings, error) {
	// Validate everything before the first write so a rejected field cannot
	// leave a half-applied patch behind.
	var siteName string
	if patch.SiteName != nil {
		siteName = strings.TrimSpace(*patch.SiteName)
		if siteName == "" || utf8.RuneCountInString(siteName) > MaxSiteNameLen {
			return Settings{}, apperr.ErrBadRequest.WithReason(apperr.ReasonInvalidValue)
		}
	}
	if patch.DefaultQuotaBytes != nil && *patch.DefaultQuotaBytes < 0 {
		return Settings{}, apperr.ErrBadRequest.WithReason(apperr.ReasonInvalidValue)
	}
	registrationChanged := false
	if patch.RegistrationEnabled != nil {
		changed, err := s.rootOnlyChange(ctx, p, sqlstore.SettingRegistrationOn, *patch.RegistrationEnabled)
		if err != nil {
			return Settings{}, err
		}
		registrationChanged = changed
	}
	deletionChanged := false
	if patch.DeletionEnabled != nil {
		changed, err := s.rootOnlyChange(ctx, p, sqlstore.SettingDeletionEnabled, *patch.DeletionEnabled)
		if err != nil {
			return Settings{}, err
		}
		deletionChanged = changed
	}
	if patch.SiteName != nil {
		if err := s.db.SetSetting(ctx, sqlstore.SettingSiteName, siteName); err != nil {
			return Settings{}, err
		}
	}
	if registrationChanged {
		v := "0"
		if *patch.RegistrationEnabled {
			v = "1"
		}
		if err := s.db.SetSetting(ctx, sqlstore.SettingRegistrationOn, v); err != nil {
			return Settings{}, err
		}
	}
	if deletionChanged {
		v := "0"
		if *patch.DeletionEnabled {
			v = "1"
		}
		if err := s.db.SetSetting(ctx, sqlstore.SettingDeletionEnabled, v); err != nil {
			return Settings{}, err
		}
	}
	if patch.DefaultQuotaBytes != nil {
		if err := s.db.SetSetting(ctx, sqlstore.SettingDefaultQuotaBytes,
			strconv.FormatInt(*patch.DefaultQuotaBytes, 10)); err != nil {
			return Settings{}, err
		}
	}
	s.audit(ctx, p, "admin.settings_update", "settings", 0, "")
	return s.GetSettings(ctx, p)
}

// rootOnlyChange reports whether key's boolean value would actually change and,
// when it would, refuses the patch unless the caller is the root
// administrator. Registration and deletion are the two site-wide switches that
// can expose the instance to strangers, so both are root-only.
func (s *Service) rootOnlyChange(ctx context.Context, p access.Principal, key string, want bool) (bool, error) {
	current, ok, err := s.db.Setting(ctx, key)
	if err != nil {
		return false, err
	}
	value := ok && (current == "1" || strings.EqualFold(current, "true"))
	if value == want {
		return false, nil
	}
	caller, err := s.db.UserByID(ctx, p.UserID)
	if err != nil {
		return false, err
	}
	if !caller.IsRoot {
		return false, apperr.ErrForbidden.WithReason(apperr.ReasonRootAdminRequired)
	}
	return true, nil
}

func (s *Service) audit(ctx context.Context, p access.Principal, action, targetType string, targetID int64, detail string) {
	ev := sqlstore.AuditEvent{
		ActorID:    p.UserID,
		ActorName:  p.Username,
		Action:     action,
		TargetType: targetType,
		TargetID:   targetID,
		Result:     "ok",
		Detail:     detail,
	}
	if err := s.db.InsertAudit(ctx, ev); err != nil {
		s.log.Warn("audit insert failed", "action", action, "err", err)
	}
}

// validUsername and validPassword delegate to the domain rules so the admin
// console, self registration and login agree on what an account may look like.
func validUsername(name string) bool { return identity.ValidUsername(name) }

func validPassword(password string) bool { return identity.ValidPassword(password) }
