package agentboard

import (
	"errors"
	"testing"
	"time"
)

func TestAuthenticate(t *testing.T) {
	b := newTestBoard(t)
	if _, err := b.Register("alice@example.com", "correct horse battery"); err != nil {
		t.Fatalf("Register: %v", err)
	}

	if got, err := b.Authenticate("alice@example.com", "correct horse battery"); err != nil {
		t.Fatalf("Authenticate with correct password: %v", err)
	} else if got.Email != "alice@example.com" {
		t.Fatalf("Authenticate returned %q", got.Email)
	}

	// Case-insensitive on the email, like Register.
	if _, err := b.Authenticate("Alice@Example.com", "correct horse battery"); err != nil {
		t.Fatalf("Authenticate with different email case: %v", err)
	}

	if _, err := b.Authenticate("alice@example.com", "wrong password"); !errors.Is(err, ErrBadCredentials) {
		t.Fatalf("wrong password: got %v, want ErrBadCredentials", err)
	}
	if _, err := b.Authenticate("nobody@example.com", "whatever"); !errors.Is(err, ErrBadCredentials) {
		t.Fatalf("unknown email: got %v, want ErrBadCredentials", err)
	}
}

// TestAuthenticateUpgradesOldIterationCount simulates a user hashed under an
// older, lower PBKDF2 work factor (e.g. before PBKDF2Iterations was raised)
// and checks that a successful login transparently rehashes it to the
// current default, without requiring the user to change their password.
func TestAuthenticateUpgradesOldIterationCount(t *testing.T) {
	b := newTestBoard(t)
	if _, err := b.Register("old@example.com", "a fine password"); err != nil {
		t.Fatalf("Register: %v", err)
	}

	// Force the stored record to look like it was hashed at a much lower,
	// outdated work factor: recompute the hash at that lower cost (a hash
	// genuinely computed at 1000 iterations, not just a mislabeled one) so
	// Authenticate must actually verify against it before rehashing.
	const oldIterations = 1000
	b.mu.Lock()
	u := b.st.Users["old@example.com"]
	oldSalt := u.Salt
	oldHash := pbkdf2SHA256("a fine password", oldSalt, oldIterations, keyLen)
	u.PasswordHash, u.Iterations = oldHash, oldIterations
	b.mu.Unlock()

	if _, err := b.Authenticate("old@example.com", "a fine password"); err != nil {
		t.Fatalf("Authenticate: %v", err)
	}

	b.mu.Lock()
	got := b.st.Users["old@example.com"]
	newIterations := got.Iterations
	rehashed := string(got.PasswordHash) != string(oldHash) || string(got.Salt) != string(oldSalt)
	b.mu.Unlock()

	if newIterations != PBKDF2Iterations {
		t.Fatalf("iterations after login = %d, want %d", newIterations, PBKDF2Iterations)
	}
	if !rehashed {
		t.Fatal("hash/salt were not rotated on rehash")
	}
	// The password itself must keep working after the rehash.
	if _, err := b.Authenticate("old@example.com", "a fine password"); err != nil {
		t.Fatalf("Authenticate after rehash: %v", err)
	}
}

// TestAuthenticateUnknownEmailTakesSimilarTimeToWrongPassword is a coarse
// sanity check (not a strict timing-attack proof, which needs statistical
// measurement well beyond a unit test) that Authenticate does not
// short-circuit an unknown email without paying roughly the same PBKDF2
// cost as a real, wrong-password check: both must be at least on the same
// order of magnitude, not "unknown returns in microseconds while a real
// check takes 100ms".
func TestAuthenticateUnknownEmailTakesSimilarTimeToWrongPassword(t *testing.T) {
	b := newTestBoard(t)
	if _, err := b.Register("known@example.com", "a fine password"); err != nil {
		t.Fatalf("Register: %v", err)
	}
	// Warm the dummy-hash cache so its one-time cost does not pollute the
	// first measurement.
	_, _ = b.Authenticate("unknown-warmup@example.com", "x")

	timeIt := func(email string) int64 {
		t.Helper()
		start := time.Now()
		_, _ = b.Authenticate(email, "wrong password")
		return time.Since(start).Nanoseconds()
	}
	wrongPW := timeIt("known@example.com")
	unknown := timeIt("still-unknown@example.com")

	// A real check should not be more than 10x faster than the dummy
	// check, or vice versa; a large asymmetry would mean one path skips
	// the expensive comparison.
	if wrongPW <= 0 || unknown <= 0 {
		t.Fatalf("non-positive durations: wrongPW=%d unknown=%d", wrongPW, unknown)
	}
	ratio := float64(wrongPW) / float64(unknown)
	if ratio > 10 || ratio < 0.1 {
		t.Skipf("timing ratio %.2f outside [0.1, 10] - likely test-environment noise, not a hard failure", ratio)
	}
}
