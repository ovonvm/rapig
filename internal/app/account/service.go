// Package account implements self-service account deletion (M5.2c): the
// request/confirm flow with its grace window, the anonymous cancel path, and the
// admin restore/purge operations. The maintenance pass that destroys accounts
// whose window expired also lives here so serve and worker share one
// implementation.
package account

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"rapig/internal/domain/access"
	"rapig/internal/domain/identity"
	sqlstore "rapig/internal/infra/sql"
	"rapig/internal/platform/apperr"
	"rapig/internal/platform/idgen"
)

const (
	// GracePeriod is how long a deleted account can still be restored with its
	// one-time token. Product decision: three days.
	GracePeriod = 72 * time.Hour
	// TombstoneRetention is how long a purged username stays reserved so a new
	// account cannot inherit the old one's identity.
	TombstoneRetention = 90 * 24 * time.Hour
	// PurgeBatch bounds how many expired accounts one maintenance pass removes.
	PurgeBatch = 100
	// CancelTokenBytes is the entropy of the one-time cancel token.
	CancelTokenBytes = 32

	// roleAdmin is the roles.code of administrator accounts (migrations/0001).
	roleAdmin = "admin"
)

// PasswordChecker re-authenticates the account owner before a destructive
// self-service action. identity.Service implements it, which keeps the argon2
// parameters and the concurrency cap in one place (and avoids an import cycle).
type PasswordChecker interface {
	CheckPassword(ctx context.Context, userID int64, password string) error
}

// Service holds the account-deletion use cases.
type Service struct {
	db        *sqlstore.DB
	log       *slog.Logger
	passwords PasswordChecker
	now       func() time.Time
}

// New builds the account service.
func New(db *sqlstore.DB, passwords PasswordChecker, log *slog.Logger) *Service {
	return &Service{db: db, log: log, passwords: passwords, now: time.Now}
}

// RequestResult is a successful deletion request: the raw cancel token (shown
// once) and when the account will be purged.
type RequestResult struct {
	CancelToken string
	PurgeAt     time.Time
}

// Enabled reports whether self-service deletion is currently open. The
// deletion_enabled setting is absent or "0" by default, which means closed.
func (s *Service) Enabled(ctx context.Context) (bool, error) {
	value, ok, err := s.db.Setting(ctx, sqlstore.SettingDeletionEnabled)
	if err != nil {
		return false, err
	}
	if !ok {
		return false, nil
	}
	return value == "1" || strings.EqualFold(value, "true"), nil
}

// Status is the caller-facing view of the deletion state: whether the feature
// is open on this instance and where the account stands. A session that is
// still usable can only ever observe the active state, because requesting a
// deletion revokes every session; the fields stay useful for clients that race
// the revocation or inspect a restored account.
type Status struct {
	Enabled    bool
	DeletedAt  *time.Time
	PurgeAfter *time.Time
}

// StatusOf reports the deletion state of the authenticated caller.
func (s *Service) StatusOf(ctx context.Context, p access.Principal) (Status, error) {
	if p.UserID <= 0 || p.Anonymous {
		return Status{}, apperr.ErrUnauthenticated
	}
	enabled, err := s.Enabled(ctx)
	if err != nil {
		return Status{}, err
	}
	user, err := s.db.UserByID(ctx, p.UserID)
	if err != nil {
		return Status{}, err
	}
	return Status{Enabled: enabled, DeletedAt: user.DeletedAt, PurgeAfter: user.PurgeAfter}, nil
}

// TombstoneExists reports whether a username is reserved by a deleted account.
// Registration and the admin console call it before creating an account.
func (s *Service) TombstoneExists(ctx context.Context, username string) (bool, error) {
	return s.db.UsernameTombstoneExists(ctx, identity.FoldUsername(strings.TrimSpace(username)), s.now().UTC())
}

// invalidToken is the single error every bad cancel token maps to, so the
// endpoint cannot be used to probe which tokens existed.
func invalidToken() error {
	return apperr.ErrBadRequest.WithReason(apperr.ReasonInvalidValue)
}

// RequestDeletion disables the caller's account for GracePeriod, revokes its
// sessions and shares, cancels active transfers and returns a one-time cancel
// token. Re-authentication with the current password is required.
func (s *Service) RequestDeletion(ctx context.Context, p access.Principal, password string) (RequestResult, error) {
	if p.UserID <= 0 || p.Anonymous {
		return RequestResult{}, apperr.ErrUnauthenticated
	}
	enabled, err := s.Enabled(ctx)
	if err != nil {
		return RequestResult{}, err
	}
	if !enabled {
		return RequestResult{}, apperr.ErrForbidden.WithReason(apperr.ReasonDeletionDisabled)
	}
	user, err := s.db.UserByID(ctx, p.UserID)
	if err != nil {
		return RequestResult{}, err
	}
	if user.IsRoot {
		return RequestResult{}, apperr.ErrForbidden.WithReason(apperr.ReasonRootAdmin)
	}
	if user.DeletionPending() {
		return RequestResult{}, apperr.ErrInvalidState.WithReason(apperr.ReasonAlreadyDeleted)
	}
	if err := s.passwords.CheckPassword(ctx, user.ID, password); err != nil {
		return RequestResult{}, err
	}

	now := s.now().UTC()
	purgeAt := now.Add(GracePeriod)
	token := idgen.MustToken(CancelTokenBytes)
	if err := s.db.BeginUserDeletion(ctx, user.ID, now, purgeAt, idgen.SHA256Hex(token)); err != nil {
		return RequestResult{}, err
	}
	s.audit(ctx, user.ID, user.Username, "auth.deletion_request", user.ID, "success",
		"purge_at="+purgeAt.Format(time.RFC3339))
	return RequestResult{CancelToken: token, PurgeAt: purgeAt}, nil
}

// CancelDeletion restores the account behind token and burns the token. It is
// the only anonymous path and therefore reveals nothing about other accounts.
func (s *Service) CancelDeletion(ctx context.Context, token string) error {
	token = strings.TrimSpace(token)
	if token == "" {
		return invalidToken()
	}
	hash := idgen.SHA256Hex(token)
	stored, err := s.db.DeletionTokenByHash(ctx, hash)
	if err != nil {
		if apperr.CodeOf(err) == apperr.CodeNotFound {
			return invalidToken()
		}
		return err
	}
	now := s.now().UTC()
	if stored.UsedAt != nil || !now.Before(stored.ExpiresAt) {
		return invalidToken()
	}
	userID, ok, err := s.db.MarkDeletionTokenUsed(ctx, hash, now)
	if err != nil {
		return err
	}
	if !ok || userID != stored.UserID {
		// A concurrent request already burned the token.
		return invalidToken()
	}
	user, err := s.db.UserByID(ctx, userID)
	if err != nil {
		if apperr.CodeOf(err) == apperr.CodeNotFound {
			return invalidToken()
		}
		return err
	}
	if err := s.db.SetUserDeletion(ctx, user.ID, identity.StatusActive, nil, nil); err != nil {
		return err
	}
	s.audit(ctx, 0, "", "auth.deletion_cancel", user.ID, "success", "username="+user.Username)
	return nil
}

// RestoreUser is the administrator recovery path: it clears the deletion window,
// re-enables the account and invalidates every outstanding cancel token.
func (s *Service) RestoreUser(ctx context.Context, p access.Principal, targetID int64) (identity.User, error) {
	target, err := s.db.UserByID(ctx, targetID)
	if err != nil {
		return identity.User{}, err
	}
	if target.ID == p.UserID {
		return identity.User{}, apperr.ErrInvalidState.WithReason(apperr.ReasonSelfTarget)
	}
	if !target.DeletionPending() {
		return identity.User{}, apperr.ErrInvalidState
	}
	now := s.now().UTC()
	if err := s.db.SetUserDeletion(ctx, target.ID, identity.StatusActive, nil, nil); err != nil {
		return identity.User{}, err
	}
	if _, err := s.db.MarkUserDeletionTokensUsed(ctx, target.ID, now); err != nil {
		return identity.User{}, err
	}
	s.audit(ctx, p.UserID, p.Username, "admin.user_restore", target.ID, "ok", "username="+target.Username)
	return s.db.UserByID(ctx, target.ID)
}

// PurgeUser destroys an account immediately, skipping the grace window, after
// the self/last-admin/root guards. The username becomes a tombstone.
func (s *Service) PurgeUser(ctx context.Context, p access.Principal, targetID int64) error {
	target, err := s.db.UserByID(ctx, targetID)
	if err != nil {
		return err
	}
	if target.ID == p.UserID {
		return apperr.ErrInvalidState.WithReason(apperr.ReasonSelfTarget)
	}
	if target.IsRoot {
		return apperr.ErrInvalidState.WithReason(apperr.ReasonRootAdmin)
	}
	if target.RoleCode == roleAdmin && target.Status == identity.StatusActive {
		others, err := s.db.CountActiveAdminsExcluding(ctx, target.ID)
		if err != nil {
			return err
		}
		if others == 0 {
			return apperr.ErrInvalidState.WithReason(apperr.ReasonLastAdmin)
		}
	}
	now := s.now().UTC()
	if err := s.db.PurgeUserAccount(ctx, target, now, now.Add(TombstoneRetention)); err != nil {
		return err
	}
	s.audit(ctx, p.UserID, p.Username, "admin.user_purge", target.ID, "ok", "username="+target.Username)
	return nil
}

// PurgeExpired destroys every account whose grace window ended and drops
// expired tombstones. It runs at startup and on the maintenance timer; one
// failing account must not stop the others.
func (s *Service) PurgeExpired(ctx context.Context) (int, int64, error) {
	now := s.now().UTC()
	due, err := s.db.UsersDueForPurge(ctx, now, PurgeBatch)
	if err != nil {
		return 0, 0, err
	}
	purged := 0
	for _, u := range due {
		if err := s.db.PurgeUserAccount(ctx, u, now, now.Add(TombstoneRetention)); err != nil {
			s.log.Warn("account: purge failed", "err", err, "user_id", u.ID)
			continue
		}
		s.audit(ctx, 0, "", "system.account_purge", u.ID, "success", "username="+u.Username)
		purged++
	}
	tombstones, err := s.db.PurgeExpiredTombstones(ctx, now)
	if err != nil {
		return purged, 0, err
	}
	return purged, tombstones, nil
}

func (s *Service) audit(ctx context.Context, actorID int64, actorName, action string, targetID int64, result, detail string) {
	ev := sqlstore.AuditEvent{
		At:         s.now().UTC(),
		ActorID:    actorID,
		ActorName:  actorName,
		Action:     action,
		TargetType: "user",
		TargetID:   targetID,
		Result:     result,
		Detail:     detail,
	}
	if err := s.db.InsertAudit(ctx, ev); err != nil {
		s.log.Warn("account: audit write failed", "err", err, "action", action)
	}
}
