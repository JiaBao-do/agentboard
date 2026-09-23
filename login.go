package agentboard

import (
	"sync"
	"time"
)

// dummyHash and dummySalt let Authenticate spend the same PBKDF2 cost on an
// unknown email as on a wrong password for a known one, so the response
// time does not leak which emails are registered. Computed once, lazily
// (not at package init, which would tax every process that never logs in).
var (
	dummyOnce            sync.Once
	dummyHash, dummySalt []byte
)

func dummyVerify(password string) {
	dummyOnce.Do(func() {
		dummyHash, dummySalt, _, _ = hashPassword("agentboard-timing-placeholder")
	})
	verifyPassword(password, dummyHash, dummySalt, PBKDF2Iterations)
}

// Authenticate verifies email and password and returns the account's public
// identity. An unknown email and a wrong password both fail with
// ErrBadCredentials, at approximately the same latency (see dummyVerify), so
// the API cannot be used to enumerate registered accounts.
//
// Like Register, the expensive PBKDF2 comparison runs outside the board
// lock: this method takes Board's mutex only to copy the small amount of
// state it needs (or, on success, to write back an upgraded hash).
func (b *Board) Authenticate(rawEmail, password string) (PublicUser, error) {
	email := normalizeEmail(rawEmail)

	b.mu.Lock()
	now := b.now().UTC()
	b.sweepLocked(now)
	u := b.st.Users[email]
	var hash, salt []byte
	var iterations int
	var created time.Time
	if u != nil {
		hash = append([]byte(nil), u.PasswordHash...)
		salt = append([]byte(nil), u.Salt...)
		iterations, created = u.Iterations, u.CreatedAt
	}
	b.mu.Unlock()

	if u == nil {
		dummyVerify(password)
		return PublicUser{}, ErrBadCredentials
	}
	if !verifyPassword(password, hash, salt, iterations) {
		return PublicUser{}, ErrBadCredentials
	}
	if iterations < PBKDF2Iterations {
		b.rehash(email, password)
	}
	return PublicUser{Email: email, CreatedAt: created}, nil
}

// User looks up an account's public identity by email, for "who am I"
// (GET /api/auth/me) once a session names it. The bool is false if no such
// account exists.
func (b *Board) User(rawEmail string) (PublicUser, bool) {
	email := normalizeEmail(rawEmail)
	b.mu.Lock()
	defer b.mu.Unlock()
	u := b.st.Users[email]
	if u == nil {
		return PublicUser{}, false
	}
	return publicUser(u), true
}

// rehash transparently upgrades a user's stored hash to the current
// PBKDF2Iterations after a successful login at an older, lower work factor.
// Best effort: a failure here does not fail the login that triggered it.
func (b *Board) rehash(email, password string) {
	hash, salt, iterations, err := hashPassword(password)
	if err != nil {
		return
	}
	_ = b.do(func(now time.Time) (*Event, error) {
		u := b.st.Users[email]
		if u == nil {
			return nil, nil
		}
		u.PasswordHash, u.Salt, u.Iterations, u.UpdatedAt = hash, salt, iterations, now
		b.log(now, systemActor, "", "user_rehash", email)
		return &Event{Type: "user"}, nil
	})
}
