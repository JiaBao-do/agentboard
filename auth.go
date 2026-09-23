package agentboard

import (
	"fmt"
	"net/mail"
	"slices"
	"strings"
	"time"

	"github.com/JiaBao-do/agentboard/model"
)

// Password and email limits. minPasswordLength follows NIST SP 800-63B,
// which recommends a minimum of 8 characters and explicitly advises against
// composition rules (must contain a digit, a symbol, ...) as they push
// users toward predictable patterns without a real security gain; length
// alone, backed by an expensive hash, is the current best practice for a
// tool at this scale. See docs/PITFALLS.md.
const (
	minPasswordLength = 8
	maxPasswordLength = 256 // bounds the PBKDF2 input; not a strength choice
	maxEmailLength    = 254 // RFC 5321 4.5.3.1.3
)

// PublicUser is a User's identity without its credential material: safe to
// return from the API, log, or keep in a session.
type PublicUser struct {
	Email     string    `json:"email"`
	CreatedAt time.Time `json:"created_at"`
}

func publicUser(u *model.User) PublicUser {
	return PublicUser{Email: u.Email, CreatedAt: u.CreatedAt}
}

func normalizeEmail(s string) string { return strings.ToLower(strings.TrimSpace(s)) }

// validateEmail normalizes and syntax-checks raw, then checks it against the
// board's configured domain allowlist (empty: every domain is allowed).
func (b *Board) validateEmail(raw string) (string, error) {
	email := normalizeEmail(raw)
	if email == "" || len(email) > maxEmailLength {
		return "", invalid("email must be 1-%d characters", maxEmailLength)
	}
	addr, err := mail.ParseAddress(email)
	if err != nil || addr.Address != email {
		return "", invalid("email %q is not a valid address", raw)
	}
	if len(b.allowedEmailDomains) > 0 {
		_, domain, ok := strings.Cut(email, "@")
		if !ok || !slices.Contains(b.allowedEmailDomains, domain) {
			return "", invalid("registration is restricted to %s", strings.Join(b.allowedEmailDomains, ", "))
		}
	}
	return email, nil
}

func validatePassword(pw string) error {
	if len(pw) < minPasswordLength || len(pw) > maxPasswordLength {
		return invalid("password must be %d-%d characters", minPasswordLength, maxPasswordLength)
	}
	return nil
}

// Register creates a new human account (email + password). The email must
// be syntactically valid and, if the board was opened with
// AllowedEmailDomains, at one of those domains. The password must be at
// least minPasswordLength characters. A duplicate email fails with
// ErrExists.
//
// Hashing runs before the board lock is taken: PBKDF2 at PBKDF2Iterations
// costs on the order of 100ms, and Board serialises every other call behind
// one mutex, so doing it under that lock would stall unrelated task and
// agent operations on every registration.
func (b *Board) Register(rawEmail, password string) (PublicUser, error) {
	email, err := b.validateEmail(rawEmail)
	if err != nil {
		return PublicUser{}, err
	}
	if err := validatePassword(password); err != nil {
		return PublicUser{}, err
	}
	hash, salt, iterations, err := hashPassword(password)
	if err != nil {
		return PublicUser{}, err
	}
	var out PublicUser
	err = b.do(func(now time.Time) (*Event, error) {
		if b.st.Users[email] != nil {
			return nil, fmt.Errorf("%w: user %s", ErrExists, email)
		}
		u := &model.User{
			Email: email, PasswordHash: hash, Salt: salt, Iterations: iterations,
			CreatedAt: now, UpdatedAt: now,
		}
		b.st.Users[email] = u
		b.log(now, systemActor, "", "user_registered", email)
		out = publicUser(u)
		return &Event{Type: "user"}, nil
	})
	return out, err
}
