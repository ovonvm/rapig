// Package identity implements authentication: bootstrap, login, session
// validation and logout.
package identity

import (
	"context"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"rapig/internal/domain/access"
	domainidentity "rapig/internal/domain/identity"
	"rapig/internal/infra/sql"
	"rapig/internal/platform/apperr"
	"rapig/internal/platform/idgen"
)

// Service is the authentication application service.
type Service struct {
	db  *sqlstore.DB
	log *slog.Logger

	// sem caps concurrent argon2 hashing so login bursts cannot exhaust the
	// 512 MB memory baseline (2 x 19 MiB + overhead is the design point).
	sem chan struct{}

	now func() time.Time

	// regLimiter throttles self registration per client IP.
	regLimiter *registerLimiter

	dummyOnce sync.Once
	dummyHash string
}

// NewService builds an authentication service.
func NewService(db *sqlstore.DB, log *slog.Logger) *Service {
	s := &Service{
		db:  db,
		log: log,
		sem: make(chan struct{}, 2),
		now: time.Now,
	}
	// The limiter shares the service clock so tests can advance time.
	s.regLimiter = newRegisterLimiter(func() time.Time { return s.now().UTC() })
	return s
}

// LoginRequest carries the inputs of a password login.
type LoginRequest struct {
	Username  string
	Password  string
	IP        string
	UserAgent string
	RequestID string
}

// LoginResult is a successful login.
type LoginResult struct {
	User         domainidentity.User
	SessionToken string
	CSRFToken    string
	ExpiresAt    time.Time
}

// Login validates credentials and starts a session. All failures return
// apperr.ErrInvalidCredentials regardless of the cause, so the endpoint does
// not leak whether an account exists or is disabled.
func (s *Service) Login(ctx context.Context, req LoginRequest) (LoginResult, error) {
	identifier := strings.TrimSpace(req.Username)
	if identifier == "" || req.Password == "" || len(req.Password) > 1024 {
		s.auditFailure(ctx, req, identifier)
		return LoginResult{}, apperr.ErrInvalidCredentials
	}

	user, err := s.lookupIdentifier(ctx, identifier)
	if err != nil {
		if e, ok := apperr.From(err); ok && e.Code == apperr.CodeNotFound {
			// Spend comparable time on unknown accounts.
			s.verifyDummy(ctx, req.Password)
			s.auditFailure(ctx, req, identifier)
			return LoginResult{}, apperr.ErrInvalidCredentials
		}
		return LoginResult{}, err
	}

	now := s.now().UTC()
	if user.Locked(now) {
		retry := int(user.LockedUntil.Sub(now).Seconds())
		if retry < 1 {
			retry = 1
		}
		s.auditFailure(ctx, req, identifier)
		return LoginResult{}, apperr.ErrTooManyAttempts.WithReason(apperr.ReasonLocked).WithRetryAfter(retry)
	}

	if err := s.verifyWithLimit(ctx, user.PasswordHash, req.Password); err != nil {
		delay := s.recordFailure(ctx, user, now)
		s.auditFailure(ctx, req, identifier)
		if delay > 0 {
			return LoginResult{}, apperr.ErrTooManyAttempts.
				WithReason(apperr.ReasonLocked).
				WithRetryAfter(int(delay.Seconds()))
		}
		return LoginResult{}, apperr.ErrInvalidCredentials
	}
	// A correct password is required before the deletion hint is revealed, so
	// the endpoint still does not leak account existence to strangers.
	if user.DeletionPending() {
		s.auditFailure(ctx, req, identifier)
		return LoginResult{}, apperr.ErrForbidden.WithReason(apperr.ReasonDeletionPending)
	}
	if !user.IsActive() {
		s.auditFailure(ctx, req, identifier)
		return LoginResult{}, apperr.ErrInvalidCredentials
	}

	sessionToken := idgen.MustToken(32)
	csrfToken := idgen.MustToken(32)
	expires := now.Add(domainidentity.SessionTTL)
	sess := domainidentity.Session{
		ID:        idgen.SHA256Hex(sessionToken),
		UserID:    user.ID,
		CSRFHash:  idgen.SHA256Hex(csrfToken),
		IP:        req.IP,
		UserAgent: truncate(req.UserAgent, 512),
		CreatedAt: now,
		ExpiresAt: expires,
	}
	if err := s.db.CreateSession(ctx, sess); err != nil {
		return LoginResult{}, err
	}
	if err := s.db.ClearLoginFailures(ctx, user.ID); err != nil {
		s.log.Warn("identity: clear login failures failed", "err", err, "user_id", user.ID)
	}
	if err := s.db.TouchLastLogin(ctx, user.ID); err != nil {
		s.log.Warn("identity: touch last login failed", "err", err, "user_id", user.ID)
	}
	s.audit(ctx, auditInput{
		actorID: user.ID, actorName: user.Username, action: "auth.login", result: "success",
		ip: req.IP, ua: req.UserAgent, reqID: req.RequestID,
	})
	return LoginResult{
		User:         user,
		SessionToken: sessionToken,
		CSRFToken:    csrfToken,
		ExpiresAt:    expires,
	}, nil
}

// lookupIdentifier resolves a login identifier through the namespace rules
// (docs/15-m5-plan.md §11): '@' means e-mail, a leading '+' or an all-digit
// string means phone, anything else is a username. An all-digit handle no
// account uses as phone is retried as a username, so numeric usernames stay
// loginable.
func (s *Service) lookupIdentifier(ctx context.Context, identifier string) (domainidentity.User, error) {
	switch domainidentity.ClassifyIdentifier(identifier) {
	case domainidentity.IdentifierEmail:
		return s.db.UserByEmail(ctx, domainidentity.FoldEmail(identifier))
	case domainidentity.IdentifierPhone:
		user, err := s.db.UserByPhone(ctx, domainidentity.FoldPhone(identifier))
		if err == nil {
			return user, nil
		}
		if apperr.CodeOf(err) != apperr.CodeNotFound {
			return domainidentity.User{}, err
		}
		return s.db.UserByUsername(ctx, strings.ToLower(identifier))
	default:
		return s.db.UserByUsername(ctx, strings.ToLower(identifier))
	}
}

// Authenticate resolves a raw session token into its user, principal and
// session. Expired or revoked sessions return apperr.ErrUnauthenticated.
func (s *Service) Authenticate(ctx context.Context, token string) (domainidentity.User, access.Principal, domainidentity.Session, error) {
	if token == "" {
		return domainidentity.User{}, access.Principal{}, domainidentity.Session{}, apperr.ErrUnauthenticated
	}
	sess, user, err := s.db.SessionByID(ctx, idgen.SHA256Hex(token))
	if err != nil {
		if apperr.CodeOf(err) == apperr.CodeNotFound {
			return domainidentity.User{}, access.Principal{}, domainidentity.Session{}, apperr.ErrUnauthenticated
		}
		return domainidentity.User{}, access.Principal{}, domainidentity.Session{}, err
	}
	if sess.Expired(s.now().UTC()) {
		if delErr := s.db.DeleteSession(ctx, sess.ID); delErr != nil {
			s.log.Warn("identity: delete expired session failed", "err", delErr, "session", sess.ID)
		}
		return domainidentity.User{}, access.Principal{}, domainidentity.Session{}, apperr.ErrUnauthenticated
	}
	if !user.IsActive() {
		return domainidentity.User{}, access.Principal{}, domainidentity.Session{}, apperr.ErrUnauthenticated
	}
	perms, err := s.db.PermissionsForRole(ctx, user.RoleID)
	if err != nil {
		return domainidentity.User{}, access.Principal{}, domainidentity.Session{}, err
	}
	// 组成员身份随会话一起装载：策略层判组资源时只看 Principal，不再回查数据库。
	groupRoles, err := s.db.GroupRolesForUser(ctx, user.ID)
	if err != nil {
		return domainidentity.User{}, access.Principal{}, domainidentity.Session{}, err
	}
	p := access.Principal{
		UserID:          user.ID,
		Username:        user.Username,
		Role:            user.RoleCode,
		Perms:           perms,
		GroupRoles:      groupRoles,
		CanCreateGroups: user.CanCreateGroups || perms.Has(access.ActionAdminUsers),
	}
	return user, p, sess, nil
}

// Logout revokes a session; an unknown token is a no-op so logout is
// idempotent.
func (s *Service) Logout(ctx context.Context, token string, req LoginRequest) error {
	if token == "" {
		return nil
	}
	id := idgen.SHA256Hex(token)
	sess, user, err := s.db.SessionByID(ctx, id)
	if err != nil {
		if apperr.CodeOf(err) == apperr.CodeNotFound {
			return nil
		}
		return err
	}
	if err := s.db.DeleteSession(ctx, sess.ID); err != nil {
		return err
	}
	s.audit(ctx, auditInput{
		actorID: user.ID, actorName: user.Username, action: "auth.logout", result: "success",
		ip: req.IP, ua: req.UserAgent, reqID: req.RequestID,
	})
	return nil
}

// CleanupSessions removes expired sessions; serve calls it at startup and
// periodically.
func (s *Service) CleanupSessions(ctx context.Context) (int64, error) {
	return s.db.DeleteExpiredSessions(ctx, s.now().UTC())
}

// recordFailure bumps the account lockout counter after a wrong password. It
// returns the lockout delay (zero when the account is not locked yet). The
// delay is derived from the new counter (docs/09-m1-plan.md §6).
func (s *Service) recordFailure(ctx context.Context, user domainidentity.User, now time.Time) time.Duration {
	count := user.FailedLoginCount + 1
	var until *time.Time
	delay := domainidentity.LockoutDelay(count)
	if delay > 0 {
		t := now.Add(delay).UTC()
		until = &t
	}
	if _, err := s.db.RecordLoginFailure(ctx, user.ID, count, until); err != nil {
		s.log.Warn("identity: record login failure failed", "err", err, "user_id", user.ID)
	}
	return delay
}

// ChangePassword verifies the current password, stores a new hash and revokes
// every other session of the account. The session that performed the change
// stays valid.
func (s *Service) ChangePassword(ctx context.Context, userID int64, sessionID, current, next string, req LoginRequest) error {
	user, err := s.db.UserByID(ctx, userID)
	if err != nil {
		return err
	}
	if err := s.verifyWithLimit(ctx, user.PasswordHash, current); err != nil {
		s.audit(ctx, auditInput{
			actorID: user.ID, actorName: user.Username, action: "auth.password_change", result: "failure",
			ip: req.IP, ua: req.UserAgent, reqID: req.RequestID, detail: "current password mismatch",
		})
		return apperr.ErrInvalidCredentials
	}
	if len(next) < 8 || len(next) > 1024 {
		return apperr.ErrBadRequest.WithReason(apperr.ReasonPasswordTooShort)
	}
	if next == current {
		return apperr.ErrBadRequest.WithReason(apperr.ReasonPasswordReuse)
	}
	hash, err := s.hashWithLimit(ctx, next)
	if err != nil {
		return err
	}
	now := s.now().UTC()
	if err := s.db.UpdatePassword(ctx, user.ID, hash, now); err != nil {
		return err
	}
	revoked, err := s.db.DeleteUserSessionsExcept(ctx, user.ID, sessionID)
	if err != nil {
		s.log.Warn("identity: revoke sessions after password change failed", "err", err, "user_id", user.ID)
	}
	s.audit(ctx, auditInput{
		actorID: user.ID, actorName: user.Username, action: "auth.password_change", result: "success",
		ip: req.IP, ua: req.UserAgent, reqID: req.RequestID,
		detail: "revoked_sessions=" + strconv.FormatInt(revoked, 10),
	})
	return nil
}

// CheckPassword verifies a raw password against one account's stored hash. It
// backs re-authentication for sensitive self-service actions (account
// deletion) and shares the argon2 concurrency cap with login, so the hashing
// parameters live in exactly one place. Mismatches map to
// apperr.ErrInvalidCredentials.
func (s *Service) CheckPassword(ctx context.Context, userID int64, password string) error {
	user, err := s.db.UserByID(ctx, userID)
	if err != nil {
		return err
	}
	if err := s.verifyWithLimit(ctx, user.PasswordHash, password); err != nil {
		return apperr.ErrInvalidCredentials
	}
	return nil
}

// hashWithLimit hashes a password under the same concurrency cap as login.
func (s *Service) hashWithLimit(ctx context.Context, password string) (string, error) {
	select {
	case s.sem <- struct{}{}:
		defer func() { <-s.sem }()
		return hashPassword(password)
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

func (s *Service) verifyWithLimit(ctx context.Context, hash, password string) error {
	select {
	case s.sem <- struct{}{}:
		defer func() { <-s.sem }()
		return verifyPassword(hash, password)
	case <-ctx.Done():
		return ctx.Err()
	}
}

// verifyDummy burns the same argon2 work as a real verification for unknown
// usernames, keeping login timing uniform.
func (s *Service) verifyDummy(ctx context.Context, password string) {
	s.dummyOnce.Do(func() {
		h, err := hashPassword("rapig-dummy-password-for-timing")
		if err != nil {
			s.log.Error("identity: cannot build dummy hash", "err", err)
			return
		}
		s.dummyHash = h
	})
	if s.dummyHash == "" {
		return
	}
	_ = s.verifyWithLimit(ctx, s.dummyHash, password)
}

type auditInput struct {
	actorID   int64
	actorName string
	action    string
	result    string
	ip        string
	ua        string
	reqID     string
	detail    string
}

func (s *Service) audit(ctx context.Context, in auditInput) {
	err := s.db.InsertAudit(ctx, sqlstore.AuditEvent{
		At:         s.now().UTC(),
		ActorID:    in.actorID,
		ActorName:  in.actorName,
		Action:     in.action,
		TargetType: "user",
		TargetID:   in.actorID,
		Result:     in.result,
		IP:         in.ip,
		UserAgent:  truncate(in.ua, 512),
		RequestID:  in.reqID,
		Detail:     in.detail,
	})
	if err != nil {
		s.log.Warn("identity: audit write failed", "err", err, "action", in.action)
	}
}

func (s *Service) auditFailure(ctx context.Context, req LoginRequest, username string) {
	s.audit(ctx, auditInput{
		actorName: username, action: "auth.login", result: "failure",
		ip: req.IP, ua: req.UserAgent, reqID: req.RequestID,
	})
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max]
}
