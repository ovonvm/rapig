// Package share is the domain model for public share links (M3.2).
package share

import "time"

// Kind is the shared node kind at creation time (snapshot; the node itself is
// re-validated on every public access).
type Kind string

const (
	KindFile Kind = "file"
	KindDir  Kind = "dir"
	// KindMulti marks a share that merges several nodes under one token.
	KindMulti Kind = "multi"
)

const (
	// MinPasswordLen / MaxPasswordLen bound optional share passwords.
	MinPasswordLen = 4
	MaxPasswordLen = 64
	// MaxDownloadsLimit is the upper bound for max_downloads.
	MaxDownloadsLimit = 1_000_000
	// TokenBytes is the entropy of the public token (32 base64url chars).
	TokenBytes = 24
	// IDBytes is the entropy of the internal share id.
	IDBytes = 12
	// TicketTTL is how long an unlocked share cookie stays valid.
	TicketTTL = time.Hour
	// StatsRetention is how long revoked/expired rows are kept for stats.
	StatsRetention = 30 * 24 * time.Hour
	// MaxItems caps how many nodes one merged share may contain (user decision
	// 2026-10-27: 100 or fewer).
	MaxItems = 100
)

// Share is one public link.
type Share struct {
	ID            string
	Token         string
	OwnerID       int64
	NodeID        int64
	Kind          Kind
	PasswordHash  string
	ExpiresAt     *time.Time
	MaxDownloads  int64
	DownloadCount int64
	VisitsCount   int64
	LastAccessAt  *time.Time
	CreatedAt     time.Time
	RevokedAt     *time.Time
	// Items is the full member set (one entry for single-node shares). It is
	// loaded by the repository; NodeID stays the primary/display node.
	Items []int64
}

// Multi reports whether the share merges several nodes.
func (s Share) Multi() bool { return s.Kind == KindMulti }

// ItemCount is the number of shared nodes (at least 1 for a persisted share).
func (s Share) ItemCount() int { return len(s.Items) }

// HasPassword reports whether the link requires a password.
func (s Share) HasPassword() bool { return s.PasswordHash != "" }

// Active reports whether the link is currently usable (not revoked, not
// expired). Node state is checked separately.
func (s Share) Active(now time.Time) bool {
	if s.RevokedAt != nil {
		return false
	}
	if s.ExpiresAt != nil && !now.Before(*s.ExpiresAt) {
		return false
	}
	return true
}

// DownloadsLeft returns the remaining downloads and whether a limit is set.
func (s Share) DownloadsLeft() (int64, bool) {
	if s.MaxDownloads == 0 {
		return 0, false
	}
	left := s.MaxDownloads - s.DownloadCount
	if left < 0 {
		left = 0
	}
	return left, true
}

// Exhausted reports whether the download budget is used up.
func (s Share) Exhausted() bool {
	left, limited := s.DownloadsLeft()
	return limited && left == 0
}
