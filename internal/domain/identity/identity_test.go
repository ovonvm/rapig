package identity

import (
	"testing"
	"time"
)

func TestLockoutDelay(t *testing.T) {
	cases := []struct {
		failures int
		want     time.Duration
	}{
		{0, 0},
		{1, 0},
		{4, 0},
		{5, 30 * time.Second},
		{6, 60 * time.Second},
		{7, 120 * time.Second},
		{8, 240 * time.Second},
		{9, 480 * time.Second},
		{10, 900 * time.Second},
		{11, 900 * time.Second},
		{50, 900 * time.Second},
	}
	for _, tc := range cases {
		if got := LockoutDelay(tc.failures); got != tc.want {
			t.Errorf("LockoutDelay(%d) = %v, want %v", tc.failures, got, tc.want)
		}
	}
}

func TestUserLocked(t *testing.T) {
	now := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	future := now.Add(time.Minute)
	past := now.Add(-time.Minute)

	if (User{}).Locked(now) {
		t.Fatal("user without locked_until must not be locked")
	}
	if !(User{LockedUntil: &future}).Locked(now) {
		t.Fatal("user with future locked_until must be locked")
	}
	if (User{LockedUntil: &past}).Locked(now) {
		t.Fatal("user with past locked_until must not be locked")
	}
}
