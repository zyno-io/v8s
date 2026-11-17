package hosted

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"sort"
	"strings"
	"sync"
	"time"
)

// Identity is who a session belongs to, as mapped from the login ID token claims.
type Identity struct {
	// OID is the provider-wide object id when the token has one (Entra: sub differs per app registration).
	OID      string
	Sub      string
	Username string
	Groups   []string
}

// Key identifies an engine: the same subject with a different group set is a different identity (visibility and
// impersonation depend on the groups).
func (i Identity) Key() string {
	g := append([]string(nil), i.Groups...)
	sort.Strings(g)
	sum := sha256.Sum256([]byte(strings.Join(g, "\x00")))
	return i.Sub + "\x00" + hex.EncodeToString(sum[:8])
}

// errNoClusterToken is wrapped so cluster.classify reports "auth-required" ("getting credentials").
var errNoClusterToken = errors.New("getting credentials: sign in again to reach this cluster")

type tokenState int

const (
	tokNone tokenState = iota
	tokOK
	tokFailed
)

// Session is one signed-in browser. Cluster tokens live here, in memory only, until logout or expiry.
type Session struct {
	id       string
	Identity Identity
	created  time.Time
	expires  time.Time // absolute

	mu         sync.Mutex
	lastSeen   time.Time
	conns      int
	closed     bool
	state      tokenState
	idToken    string
	refresh    string
	tokenExp   time.Time
	refreshing bool
	closers    map[int]func()
	nextCloser int
}

// ID returns the session id (a credential: never log it).
func (s *Session) ID() string { return s.id }

// Expires returns the absolute expiry.
func (s *Session) Expires() time.Time { return s.expires }

// SetClusterTokens stores the cluster app's ID token and refresh token.
func (s *Session) SetClusterTokens(idToken, refresh string, exp time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	s.idToken, s.refresh, s.tokenExp, s.state, s.refreshing = idToken, refresh, exp, tokOK, false
}

// MarkAuthRequired drops the cluster tokens: the user's clusters need a new sign-in.
func (s *Session) MarkAuthRequired() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.idToken, s.refresh, s.state, s.refreshing = "", "", tokFailed, false
}

// ClusterToken returns the current cluster ID token, or an error that reads as "auth-required".
func (s *Session) ClusterToken(now time.Time) (string, time.Time, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.state != tokOK || s.idToken == "" || !now.Before(s.tokenExp) {
		return "", time.Time{}, errNoClusterToken
	}
	return s.idToken, s.tokenExp, nil
}

// ClusterAuthOK reports whether the session currently holds a usable cluster token.
func (s *Session) ClusterAuthOK(now time.Time) bool {
	_, _, err := s.ClusterToken(now)
	return err == nil
}

// beginRefresh returns the refresh token when the token expires within skew and no refresh is in flight.
func (s *Session) beginRefresh(now time.Time, skew time.Duration) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.state != tokOK || s.refreshing || s.refresh == "" || now.Add(skew).Before(s.tokenExp) {
		return "", false
	}
	s.refreshing = true
	return s.refresh, true
}

func (s *Session) endRefresh() {
	s.mu.Lock()
	s.refreshing = false
	s.mu.Unlock()
}

// OnClose registers f to run when the session ends (logout, expiry, eviction). It returns a remover. If the
// session already ended f runs immediately.
func (s *Session) OnClose(f func()) (remove func()) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		f()
		return func() {}
	}
	if s.closers == nil {
		s.closers = map[int]func(){}
	}
	n := s.nextCloser
	s.nextCloser++
	s.closers[n] = f
	s.mu.Unlock()
	return func() { s.mu.Lock(); delete(s.closers, n); s.mu.Unlock() }
}

// TryOnClose registers f like OnClose but never runs it inline: it reports false when the session already ended
// (callers hold their own locks, which f may need).
func (s *Session) TryOnClose(f func()) (remove func(), ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, false
	}
	if s.closers == nil {
		s.closers = map[int]func(){}
	}
	n := s.nextCloser
	s.nextCloser++
	s.closers[n] = f
	return func() { s.mu.Lock(); delete(s.closers, n); s.mu.Unlock() }, true
}

func (s *Session) connOpened() { s.mu.Lock(); s.conns++; s.mu.Unlock() }
func (s *Session) connClosed(now time.Time) {
	s.mu.Lock()
	if s.conns > 0 {
		s.conns--
	}
	s.lastSeen = now
	s.mu.Unlock()
}

// destroy wipes the tokens and runs the close hooks (once).
func (s *Session) destroy() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	s.idToken, s.refresh, s.state = "", "", tokNone
	fs := make([]func(), 0, len(s.closers))
	for _, f := range s.closers {
		fs = append(fs, f)
	}
	s.closers = nil
	s.mu.Unlock()
	for _, f := range fs {
		f()
	}
}

// Store keeps the sessions in memory.
type Store struct {
	clock    Clock
	lifetime time.Duration
	idle     time.Duration
	max      int

	mu   sync.Mutex
	byID map[string]*Session
}

func NewStore(clock Clock, lifetime, idle time.Duration, maxPerUser int) *Store {
	return &Store{clock: clock, lifetime: lifetime, idle: idle, max: maxPerUser, byID: map[string]*Session{}}
}

func newSessionID() string {
	b := make([]byte, 32) // 256 bits
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

// Create makes a session for id, evicting the user's oldest sessions beyond the per-user limit.
func (st *Store) Create(id Identity) *Session {
	now := st.clock.Now()
	s := &Session{id: newSessionID(), Identity: id, created: now, expires: now.Add(st.lifetime), lastSeen: now}
	var evict []*Session
	st.mu.Lock()
	st.byID[s.id] = s
	if st.max > 0 {
		var mine []*Session
		for _, o := range st.byID {
			if o.Identity.Sub == id.Sub {
				mine = append(mine, o)
			}
		}
		if len(mine) > st.max {
			sort.Slice(mine, func(i, j int) bool { return mine[i].created.Before(mine[j].created) })
			for _, o := range mine[:len(mine)-st.max] {
				delete(st.byID, o.id)
				evict = append(evict, o)
			}
		}
	}
	st.mu.Unlock()
	for _, o := range evict {
		o.destroy()
	}
	return s
}

func (st *Store) expired(s *Session, now time.Time) bool {
	if !now.Before(s.expires) {
		return true
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.conns == 0 && now.Sub(s.lastSeen) > st.idle
}

// Lookup returns the live session for an id and records activity. Expired sessions are deleted.
func (st *Store) Lookup(id string) (*Session, bool) {
	if id == "" {
		return nil, false
	}
	now := st.clock.Now()
	st.mu.Lock()
	s := st.byID[id]
	if s == nil {
		st.mu.Unlock()
		return nil, false
	}
	if st.expired(s, now) {
		delete(st.byID, id)
		st.mu.Unlock()
		s.destroy()
		return nil, false
	}
	st.mu.Unlock()
	s.mu.Lock()
	s.lastSeen = now
	s.mu.Unlock()
	return s, true
}

// Delete ends a session (logout).
func (st *Store) Delete(id string) {
	st.mu.Lock()
	s := st.byID[id]
	delete(st.byID, id)
	st.mu.Unlock()
	if s != nil {
		s.destroy()
	}
}

// Sweep ends every expired or idle session and returns the live ones.
func (st *Store) Sweep() []*Session {
	now := st.clock.Now()
	var dead, live []*Session
	st.mu.Lock()
	for id, s := range st.byID {
		if st.expired(s, now) {
			delete(st.byID, id)
			dead = append(dead, s)
		} else {
			live = append(live, s)
		}
	}
	st.mu.Unlock()
	for _, s := range dead {
		s.destroy()
	}
	return live
}

// Len is the number of stored sessions.
func (st *Store) Len() int { st.mu.Lock(); defer st.mu.Unlock(); return len(st.byID) }
