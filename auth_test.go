package agentboard

import (
	"errors"
	"testing"
)

func newTestBoard(t *testing.T, domains ...string) *Board {
	t.Helper()
	b, err := Open(Options{AllowedEmailDomains: domains})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = b.Close(t.Context()) })
	return b
}

func TestRegister(t *testing.T) {
	b := newTestBoard(t)

	u, err := b.Register("Alice@Example.com", "correct horse battery")
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	if u.Email != "alice@example.com" {
		t.Fatalf("email not normalized to lower case: %q", u.Email)
	}
	if u.CreatedAt.IsZero() {
		t.Fatal("CreatedAt not set")
	}

	// The stored record must never carry the plaintext password or
	// anything reversible to it - only a hash and a per-user salt.
	b.mu.Lock()
	stored := b.st.Users["alice@example.com"]
	b.mu.Unlock()
	if stored == nil {
		t.Fatal("user not stored")
	}
	if len(stored.PasswordHash) == 0 || len(stored.Salt) < 16 {
		t.Fatalf("hash/salt look wrong: hash=%d bytes salt=%d bytes", len(stored.PasswordHash), len(stored.Salt))
	}
	if string(stored.PasswordHash) == "correct horse battery" {
		t.Fatal("password stored in the clear")
	}
	if stored.Iterations != PBKDF2Iterations {
		t.Fatalf("iterations = %d, want %d", stored.Iterations, PBKDF2Iterations)
	}
}

func TestRegisterDuplicateEmail(t *testing.T) {
	b := newTestBoard(t)
	if _, err := b.Register("dup@example.com", "first password!"); err != nil {
		t.Fatalf("first Register: %v", err)
	}
	// Case must not matter for the uniqueness check either.
	if _, err := b.Register("DUP@example.com", "second password!"); !errors.Is(err, ErrExists) {
		t.Fatalf("duplicate email: got %v, want ErrExists", err)
	}
}

func TestRegisterValidation(t *testing.T) {
	b := newTestBoard(t)
	cases := []struct {
		name, email, password string
	}{
		{"bad email", "not-an-email", "a fine password"},
		{"empty email", "", "a fine password"},
		{"short password", "shortpw@example.com", "1234567"}, // 7 chars, below the minimum
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := b.Register(c.email, c.password); !errors.Is(err, ErrInvalid) {
				t.Fatalf("got %v, want ErrInvalid", err)
			}
		})
	}
}

func TestRegisterDomainAllowlist(t *testing.T) {
	b := newTestBoard(t, "example.com", "example.org")

	if _, err := b.Register("dev@example.com", "a fine password"); err != nil {
		t.Fatalf("allowed domain rejected: %v", err)
	}
	if _, err := b.Register("dev@Example.ORG", "a fine password"); err != nil {
		t.Fatalf("allowed domain (different case) rejected: %v", err)
	}
	if _, err := b.Register("dev@notallowed.com", "a fine password"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("disallowed domain: got %v, want ErrInvalid", err)
	}
}

func TestRegisterNoAllowlistAcceptsAnyDomain(t *testing.T) {
	b := newTestBoard(t) // no AllowedEmailDomains configured
	if _, err := b.Register("someone@anything-at-all.example", "a fine password"); err != nil {
		t.Fatalf("empty allowlist should accept any domain: %v", err)
	}
}
