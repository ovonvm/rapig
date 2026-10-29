package identity

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	domainidentity "rapig/internal/domain/identity"
	sqlstore "rapig/internal/infra/sql"
	"rapig/internal/platform/apperr"
	"rapig/internal/platform/idgen"
)

const (
	// RegistrationIPLimit is how many accounts a single client IP may create
	// inside RegistrationWindow.
	RegistrationIPLimit = 5
	// RegistrationWindow is the sliding window of the registration limiter.
	RegistrationWindow = time.Hour
	// DefaultRegistrationQuotaBytes is used when the default_quota_bytes
	// setting is missing or unreadable. The 0012 migration seeds the same
	// value (docs/15-m5-plan.md §13: 注册默认配额 1 GB).
	DefaultRegistrationQuotaBytes int64 = 1 << 30

	// generatedUsernamePrefix and generatedUsernameLen describe the fallback
	// handle (u + 8 lowercase alphanumerics) used when a registration only
	// carries an e-mail or a phone number.
	generatedUsernamePrefix = "u"
	generatedUsernameLen    = 8
	// usernameGenerationAttempts bounds collisions on the generated handle.
	usernameGenerationAttempts = 5
)

// RegisterRequest is a POST /auth/register payload plus request metadata.
// Username is optional: at least one of the three identifiers must be set.
type RegisterRequest struct {
	Username  string
	Email     string
	Phone     string
	Password  string
	IP        string
	UserAgent string
	RequestID string
}

var (
	errIdentifierRequired = apperr.New(apperr.CodeBadRequest, http.StatusBadRequest,
		"请至少填写用户名、邮箱或手机号中的一项").WithReason(apperr.ReasonInvalidValue)
	errRegisterEmail = apperr.New(apperr.CodeBadRequest, http.StatusBadRequest,
		"邮箱格式不正确").WithReason(apperr.ReasonInvalidValue)
	errRegisterPhone = apperr.New(apperr.CodeBadRequest, http.StatusBadRequest,
		"手机号格式不正确（6–20 位数字，可带 + 前缀）").WithReason(apperr.ReasonInvalidValue)
	errRegisterUsername = apperr.New(apperr.CodeInvalidName, http.StatusBadRequest,
		"用户名需为 2–32 个字符，只能包含字母、数字、点、下划线和短横线").WithReason(apperr.ReasonInvalidValue)
)

// RegistrationEnabled reports whether self registration is open. The setting is
// absent by default, which means closed.
func (s *Service) RegistrationEnabled(ctx context.Context) (bool, error) {
	value, ok, err := s.db.Setting(ctx, sqlstore.SettingRegistrationOn)
	if err != nil {
		return false, err
	}
	if !ok {
		return false, nil
	}
	return value == "1" || strings.EqualFold(value, "true"), nil
}

// Register creates a self-registered account. It never starts a session: the
// client is expected to log in afterwards (docs/15-m5-plan.md §11).
func (s *Service) Register(ctx context.Context, req RegisterRequest) (domainidentity.User, error) {
	enabled, err := s.RegistrationEnabled(ctx)
	if err != nil {
		return domainidentity.User{}, err
	}
	if !enabled {
		return domainidentity.User{}, apperr.ErrForbidden.WithReason(apperr.ReasonRegistrationDisabled)
	}
	if allowed, retry := s.regLimiter.allow(req.IP); !allowed {
		s.audit(ctx, auditInput{
			actorName: "anonymous", action: "auth.register", result: "failure",
			ip: req.IP, ua: req.UserAgent, reqID: req.RequestID, detail: "rate_limited",
		})
		limited := apperr.ErrTooManyAttempts.WithReason(apperr.ReasonRateLimited)
		if retry > 0 {
			limited = limited.WithRetryAfter(retry)
		}
		return domainidentity.User{}, limited
	}

	username := strings.TrimSpace(req.Username)
	email, emailFolded, emailOK := domainidentity.NormalizeEmail(req.Email)
	if !emailOK {
		return domainidentity.User{}, errRegisterEmail
	}
	phone, phoneFolded, phoneOK := domainidentity.NormalizePhone(req.Phone)
	if !phoneOK {
		return domainidentity.User{}, errRegisterPhone
	}
	if username != "" && !domainidentity.ValidUsername(username) {
		return domainidentity.User{}, errRegisterUsername
	}
	if username == "" && email == "" && phone == "" {
		return domainidentity.User{}, errIdentifierRequired
	}
	if !domainidentity.ValidPassword(req.Password) {
		return domainidentity.User{}, apperr.ErrBadRequest.WithReason(apperr.ReasonPasswordTooShort)
	}

	roleID, err := s.db.RoleIDByCode(ctx, "user")
	if err != nil {
		return domainidentity.User{}, err
	}
	hash, err := s.hashWithLimit(ctx, req.Password)
	if err != nil {
		return domainidentity.User{}, err
	}
	quota := s.defaultQuota(ctx)

	generated := username == ""
	attempts := 1
	if generated {
		attempts = usernameGenerationAttempts
	}
	for i := 0; i < attempts; i++ {
		if generated {
			suffix, genErr := idgen.LowerAlnum(generatedUsernameLen)
			if genErr != nil {
				return domainidentity.User{}, genErr
			}
			username = generatedUsernamePrefix + suffix
		}
		folded := domainidentity.FoldUsername(username)
		reserved, err := s.db.UsernameTombstoneExists(ctx, folded, s.now().UTC())
		if err != nil {
			return domainidentity.User{}, err
		}
		if reserved {
			// A deleted account's name stays reserved for a while so a new
			// account cannot inherit its shares or impersonate it.
			if generated {
				username = ""
				continue
			}
			return domainidentity.User{}, apperr.ErrNameConflict.WithReason(apperr.ReasonUsernameReserved)
		}
		taken, err := s.db.IdentifierTaken(ctx, folded, emailFolded, phoneFolded)
		if err != nil {
			return domainidentity.User{}, err
		}
		if taken {
			if generated {
				username = ""
				continue
			}
			return domainidentity.User{}, apperr.ErrNameConflict.WithReason(apperr.ReasonIdentifierTaken)
		}
		user, err := s.db.CreateUser(ctx, domainidentity.User{
			Username:       username,
			UsernameFolded: folded,
			DisplayName:    username,
			PasswordHash:   hash,
			RoleID:         roleID,
			Status:         domainidentity.StatusActive,
			QuotaBytes:     quota,
			Email:          email,
			EmailFolded:    emailFolded,
			Phone:          phone,
			PhoneFolded:    phoneFolded,
		})
		if err != nil {
			// A concurrent registration with the same identifier loses the
			// race at the unique index; treat it like a duplicate.
			if isUniqueViolation(err) {
				if generated {
					username = ""
					continue
				}
				return domainidentity.User{}, apperr.ErrNameConflict.WithReason(apperr.ReasonIdentifierTaken)
			}
			return domainidentity.User{}, err
		}
		s.audit(ctx, auditInput{
			actorID: user.ID, actorName: user.Username, action: "auth.register", result: "success",
			ip: req.IP, ua: req.UserAgent, reqID: req.RequestID,
		})
		return user, nil
	}
	return domainidentity.User{}, apperr.ErrNameConflict.WithReason(apperr.ReasonIdentifierTaken)
}

// defaultQuota is the quota a self-registered account starts with: the
// admin-configurable default_quota_bytes setting (0 means unlimited), falling
// back to 1 GiB when the setting is unreadable.
func (s *Service) defaultQuota(ctx context.Context) int64 {
	value, ok, err := s.db.Setting(ctx, sqlstore.SettingDefaultQuotaBytes)
	if err != nil {
		s.log.Warn("identity: read default quota failed", "err", err)
		return DefaultRegistrationQuotaBytes
	}
	if !ok {
		return DefaultRegistrationQuotaBytes
	}
	n, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
	if err != nil || n < 0 {
		return DefaultRegistrationQuotaBytes
	}
	return n
}

// isUniqueViolation reports whether err is a SQLite UNIQUE constraint failure.
func isUniqueViolation(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "unique constraint")
}

// registerLimiter is an in-memory sliding window keyed by client IP. rapig runs
// as a single process and the limit only has to slow down account farming, so
// no persistence is needed: a restart clears the counters.
type registerLimiter struct {
	mu        sync.Mutex
	hits      map[string][]time.Time
	now       func() time.Time
	lastSweep time.Time
}

func newRegisterLimiter(now func() time.Time) *registerLimiter {
	return &registerLimiter{hits: make(map[string][]time.Time), now: now}
}

// allow records a hit for ip and reports whether it stays inside the limit,
// plus a Retry-After hint in seconds when it does not.
func (l *registerLimiter) allow(ip string) (bool, int) {
	if ip == "" {
		ip = "unknown"
	}
	now := l.now().UTC()
	cutoff := now.Add(-RegistrationWindow)
	l.mu.Lock()
	defer l.mu.Unlock()
	l.sweepLocked(now, cutoff)
	kept := pruneHits(l.hits[ip], cutoff)
	if len(kept) >= RegistrationIPLimit {
		l.hits[ip] = kept
		retry := int(kept[0].Add(RegistrationWindow).Sub(now).Seconds()) + 1
		if retry < 1 {
			retry = 1
		}
		return false, retry
	}
	l.hits[ip] = append(kept, now)
	return true, 0
}

// sweepLocked drops IPs whose whole window expired, so the map cannot grow
// without bound.
func (l *registerLimiter) sweepLocked(now, cutoff time.Time) {
	if now.Sub(l.lastSweep) < RegistrationWindow {
		return
	}
	for key, times := range l.hits {
		if kept := pruneHits(times, cutoff); len(kept) == 0 {
			delete(l.hits, key)
		} else {
			l.hits[key] = kept
		}
	}
	l.lastSweep = now
}

func pruneHits(times []time.Time, cutoff time.Time) []time.Time {
	kept := times[:0]
	for _, t := range times {
		if t.After(cutoff) {
			kept = append(kept, t)
		}
	}
	return kept
}
