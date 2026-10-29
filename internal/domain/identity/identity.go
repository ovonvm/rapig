package identity

import "time"

// User status values.
const (
	StatusActive   = "active"
	StatusDisabled = "disabled"
)

// SessionTTL is how long a login session stays valid.
const SessionTTL = 7 * 24 * time.Hour

// Login lockout policy (docs/09-m1-plan.md §6): after MaxLoginAttempts
// consecutive failures the account is locked for LockoutBase, doubling per
// further failure up to LockoutMax. The delay is computed from the failure
// count, no scheduler is involved.
const (
	MaxLoginAttempts = 5
	LockoutBase      = 30 * time.Second
	LockoutMax       = 15 * time.Minute
)

// LockoutDelay returns how long an account with the given failure count stays
// locked. Counts below MaxLoginAttempts never lock.
func LockoutDelay(failures int) time.Duration {
	if failures < MaxLoginAttempts {
		return 0
	}
	delay := LockoutBase
	for i := MaxLoginAttempts; i < failures; i++ {
		delay *= 2
		if delay >= LockoutMax {
			return LockoutMax
		}
	}
	return delay
}

// User is an account row.
type User struct {
	ID                int64
	Username          string
	UsernameFolded    string
	DisplayName       string
	PasswordHash      string
	RoleID            int64
	RoleCode          string
	Status            string
	UsedBytes         int64
	QuotaBytes        int64
	ReservedBytes     int64
	FailedLoginCount  int
	CreatedAt         time.Time
	UpdatedAt         time.Time
	LastLoginAt       *time.Time
	LockedUntil       *time.Time
	PasswordChangedAt *time.Time

	// CanCreateGroups 是 P1 的建组能力：只有被系统 admin 授予该能力的用户
	// 才能创建组（建组者自动成为组 manager）。系统 admin 走 permission 判断，
	// 不受这个开关限制。
	CanCreateGroups bool

	// Email and Phone are optional second/third identifiers (M5.2b). The raw
	// values keep the case the user typed; the folded values are what the
	// uniqueness index and login lookups use. Empty means "not set".
	Email       string
	EmailFolded string
	Phone       string
	PhoneFolded string
	// IsRoot marks the single built-in root administrator, the only account
	// allowed to flip registration on or off.
	IsRoot bool

	// DeletedAt and PurgeAfter mark a self-service deletion awaiting purge
	// (M5.2c). DeletedAt is set while the account is inside its grace window
	// and PurgeAfter is when the maintenance job destroys the row. Both nil for
	// a live account; the row only disappears after purging so a cancelled
	// deletion leaves no trace.
	DeletedAt  *time.Time
	PurgeAfter *time.Time
}

// DeletionPending reports whether the account is inside a deletion grace
// window.
func (u User) DeletionPending() bool { return u.DeletedAt != nil }

// IsActive reports whether the account may log in.
func (u User) IsActive() bool { return u.Status == StatusActive }

// Locked reports whether the account is inside a lockout window at now.
func (u User) Locked(now time.Time) bool {
	return u.LockedUntil != nil && now.Before(*u.LockedUntil)
}

// Session is an authenticated login. ID is sha256hex(token); the raw token is
// only stored in the client cookie.
type Session struct {
	ID        string
	UserID    int64
	CSRFHash  string
	IP        string
	UserAgent string
	CreatedAt time.Time
	ExpiresAt time.Time
}

// Expired reports whether the session is past its expiry at now.
func (s Session) Expired(now time.Time) bool { return !now.Before(s.ExpiresAt) }
