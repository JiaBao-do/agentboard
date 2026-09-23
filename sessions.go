package agentboard

import (
	"crypto/rand"
	"encoding/hex"
	"sync"
	"time"
)

// sessionIDBytes is the random session ID length: 32 bytes (256 bits) of
// crypto/rand, hex-encoded to a 64-character opaque token. This is a plain
// random identifier into a server-side table, not a signed or encoded
// credential (no JWT): the only way to forge one is to guess 256 random
// bits, which is not a JSON web token dependency this zero-dependency
// project would need to add.
const sessionIDBytes = 32

// session is one logged-in browser session.
type session struct {
	email     string
	expiresAt time.Time
}

// sessionStore is an in-memory, process-local table of logged-in sessions,
// keyed by a random session ID. It is deliberately not persisted to the
// Store: a restart logs every browser out, which is an acceptable, explicit
// trade-off for a tool built for solo/trusted-team localhost use (see
// docs/PITFALLS.md) and avoids adding session signing/persistence
// complexity, or a JWT dependency, to a zero-dependency project. Safe for
// concurrent use.
type sessionStore struct {
	mu   sync.Mutex
	ttl  time.Duration
	now  func() time.Time
	byID map[string]*session
}

// defaultSessionTTL is how long a session stays valid after its last use
// (a sliding window, extended on every successful validate). 24 hours
// balances not forcing a daily re-login for an actively used board against
// not leaving a session valid indefinitely on a shared or borrowed machine.
const defaultSessionTTL = 24 * time.Hour

func newSessionStore(ttl time.Duration, now func() time.Time) *sessionStore {
	if ttl <= 0 {
		ttl = defaultSessionTTL
	}
	if now == nil {
		now = time.Now
	}
	return &sessionStore{ttl: ttl, now: now, byID: map[string]*session{}}
}

func newSessionID() (string, error) {
	b := make([]byte, sessionIDBytes)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// create starts a new session for email and returns its opaque ID.
func (s *sessionStore) create(email string) (string, error) {
	id, err := newSessionID()
	if err != nil {
		return "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.byID[id] = &session{email: email, expiresAt: s.now().Add(s.ttl)}
	return id, nil
}

// validate reports whether id names a live, unexpired session and, if so,
// returns its email and slides the expiry forward by ttl from now. An
// expired session is deleted immediately rather than left for the next
// sweep, so it can never be validated again by chance.
func (s *sessionStore) validate(id string) (string, bool) {
	if id == "" {
		return "", false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	sess := s.byID[id]
	if sess == nil {
		return "", false
	}
	now := s.now()
	if now.After(sess.expiresAt) {
		delete(s.byID, id)
		return "", false
	}
	sess.expiresAt = now.Add(s.ttl)
	return sess.email, true
}

// invalidate ends one session (logout). Ending an unknown or already-ended
// session is not an error: logout is idempotent.
func (s *sessionStore) invalidate(id string) {
	if id == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.byID, id)
}

// sweep removes sessions that expired without being revisited, so an idle
// server does not accumulate them forever. The server calls it
// periodically; it is safe to skip (every validate cleans up its own
// expired entry).
func (s *sessionStore) sweep() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	n := 0
	for id, sess := range s.byID {
		if now.After(sess.expiresAt) {
			delete(s.byID, id)
			n++
		}
	}
	return n
}

func (s *sessionStore) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.byID)
}

// loginLimiter is a simple, in-memory, best-effort guard against a naive
// scripted brute-force login: it is NOT a substitute for a persistent,
// distributed rate limiter or a WAF, and it is intentionally scoped to
// match this tool's localhost/trusted-team threat model (see
// docs/PITFALLS.md for exactly what it does and does not defend against).
// It tracks failures per normalized email, not per source address: the
// goal is "an attacker who knows or guesses one email cannot hammer that
// one account", not IP-based throttling, which is unreliable on a server
// that may sit behind a reverse proxy with no trusted X-Forwarded-For.
type loginLimiter struct {
	mu       sync.Mutex
	failures map[string][]time.Time
	now      func() time.Time
}

const (
	// loginMaxFailures is how many failed attempts within loginWindow are
	// allowed before further attempts for that email are refused.
	loginMaxFailures = 10
	loginWindow      = 15 * time.Minute
)

func newLoginLimiter(now func() time.Time) *loginLimiter {
	if now == nil {
		now = time.Now
	}
	return &loginLimiter{failures: map[string][]time.Time{}, now: now}
}

// allowed reports whether email has fewer than loginMaxFailures recorded
// failures within the trailing loginWindow, pruning older entries as it
// goes so the map does not grow without bound.
func (l *loginLimiter) allowed(email string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	cut := l.now().Add(-loginWindow)
	kept := l.failures[email][:0]
	for _, t := range l.failures[email] {
		if t.After(cut) {
			kept = append(kept, t)
		}
	}
	if len(kept) == 0 {
		delete(l.failures, email)
	} else {
		l.failures[email] = kept
	}
	return len(kept) < loginMaxFailures
}

func (l *loginLimiter) recordFailure(email string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.failures[email] = append(l.failures[email], l.now())
}

func (l *loginLimiter) recordSuccess(email string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.failures, email)
}
