package agentboard

import (
	"testing"
	"time"
)

func TestSessionStoreLifecycle(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	s := newSessionStore(time.Hour, clock)

	id, err := s.create("alice@example.com")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if len(id) != sessionIDBytes*2 { // hex-encoded
		t.Fatalf("session id is %d chars, want %d (hex of %d random bytes)", len(id), sessionIDBytes*2, sessionIDBytes)
	}

	if email, ok := s.validate(id); !ok || email != "alice@example.com" {
		t.Fatalf("validate = %q, %v", email, ok)
	}

	// Two sessions for the same email must get different, unguessable IDs.
	id2, err := s.create("alice@example.com")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if id2 == id {
		t.Fatal("two sessions got the same ID")
	}

	s.invalidate(id)
	if _, ok := s.validate(id); ok {
		t.Fatal("invalidated session still validates")
	}
	if _, ok := s.validate(id2); !ok {
		t.Fatal("invalidating one session invalidated an unrelated one")
	}

	if _, ok := s.validate("not-a-real-session-id"); ok {
		t.Fatal("an unknown session id validated")
	}
	if _, ok := s.validate(""); ok {
		t.Fatal("an empty session id validated")
	}
}

func TestSessionStoreSlidingExpiry(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	s := newSessionStore(time.Hour, clock)

	id, err := s.create("alice@example.com")
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	// Just before expiry, a validate call must both succeed and extend the
	// deadline another full TTL forward.
	now = now.Add(59 * time.Minute)
	if _, ok := s.validate(id); !ok {
		t.Fatal("session expired before its TTL elapsed")
	}
	now = now.Add(59 * time.Minute) // would be 118 min since create, past a fixed 60 min TTL
	if _, ok := s.validate(id); !ok {
		t.Fatal("sliding expiry did not extend the session on activity")
	}

	// Now let it sit idle past the TTL with no activity.
	now = now.Add(61 * time.Minute)
	if _, ok := s.validate(id); ok {
		t.Fatal("session did not expire after sitting idle past its TTL")
	}
}

func TestSessionStoreSweep(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	s := newSessionStore(time.Hour, clock)

	live, err := s.create("live@example.com")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := s.create("expiring@example.com"); err != nil {
		t.Fatalf("create: %v", err)
	}

	now = now.Add(30 * time.Minute)
	if _, ok := s.validate(live); !ok { // keep "live" alive with activity
		t.Fatal("live session should still validate")
	}
	now = now.Add(45 * time.Minute) // "expiring" is now well past its TTL; "live" was refreshed 45 min ago, still within TTL

	if n := s.sweep(); n != 1 {
		t.Fatalf("sweep removed %d sessions, want 1", n)
	}
	if s.count() != 1 {
		t.Fatalf("count = %d, want 1 (only the live session)", s.count())
	}
	if _, ok := s.validate(live); !ok {
		t.Fatal("sweep removed the still-live session")
	}
}

func TestLoginLimiter(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	l := newLoginLimiter(clock)

	for i := 0; i < loginMaxFailures; i++ {
		if !l.allowed("victim@example.com") {
			t.Fatalf("blocked after only %d failures, want %d", i, loginMaxFailures)
		}
		l.recordFailure("victim@example.com")
	}
	if l.allowed("victim@example.com") {
		t.Fatalf("still allowed after %d failures", loginMaxFailures)
	}

	// A different account must be unaffected.
	if !l.allowed("bystander@example.com") {
		t.Fatal("an unrelated account was blocked")
	}

	// A success clears the counter.
	l.recordSuccess("victim@example.com")
	if !l.allowed("victim@example.com") {
		t.Fatal("a successful login did not reset the failure count")
	}

	// Failures outside the window are not counted.
	for i := 0; i < loginMaxFailures; i++ {
		l.recordFailure("stale@example.com")
	}
	now = now.Add(loginWindow + time.Minute)
	if !l.allowed("stale@example.com") {
		t.Fatal("old failures outside the window still counted")
	}
}
