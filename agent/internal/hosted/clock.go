// Package hosted is the self-hosted multi-user mode (`v8s server`): OIDC login, server-side sessions, per-user
// engines whose cluster credentials are the user's own tokens (or admin-configured impersonation/kiosk), and the
// cookie-authenticated WebSocket front door. See docs/auth.md and docs/protocol.md (Hosted).
package hosted

import "time"

// Clock is the time source; injectable so tests control lifetimes, refreshes and the 30 s engine grace.
type Clock interface {
	Now() time.Time
	// AfterFunc calls f after d unless stopped.
	AfterFunc(d time.Duration, f func()) (stop func() bool)
}

// RealClock is the wall clock.
type RealClock struct{}

func (RealClock) Now() time.Time { return time.Now() }
func (RealClock) AfterFunc(d time.Duration, f func()) func() bool {
	t := time.AfterFunc(d, f)
	return t.Stop
}
