package agentboard

import (
	"bytes"
	"encoding/hex"
	"testing"
)

// TestPBKDF2SHA256KnownVectors checks pbkdf2SHA256 against reference values
// computed independently with Python's stdlib hashlib.pbkdf2_hmac (not this
// implementation, not golang.org/x/crypto/pbkdf2), so a bug shared between
// "implementation" and "test" cannot hide. These are also the widely
// published PBKDF2-HMAC-SHA256 test vectors for password "password", salt
// "salt" (e.g. RFC 7914 style vectors used across pbkdf2 test suites).
func TestPBKDF2SHA256KnownVectors(t *testing.T) {
	cases := []struct {
		iterations int
		want       string
	}{
		{1, "120fb6cffcf8b32c43e7225256c4f837a86548c92ccc35480805987cb70be17b"},
		{2, "ae4d0c95af6b46d32d0adff928f06dd02a303f8ef3c251dfd6e2d85a95474c43"},
		{4096, "c5e478d59288c841aa530db6845c4c8d962893a001ce4e11a4963873aa98134a"},
	}
	for _, c := range cases {
		want, err := hex.DecodeString(c.want)
		if err != nil {
			t.Fatalf("bad test vector: %v", err)
		}
		got := pbkdf2SHA256("password", []byte("salt"), c.iterations, len(want))
		if !bytes.Equal(got, want) {
			t.Errorf("iterations=%d: got %x, want %x", c.iterations, got, want)
		}
	}
}

func TestHashPasswordUniqueSaltAndHash(t *testing.T) {
	h1, s1, it1, err := hashPassword("correct horse battery staple")
	if err != nil {
		t.Fatalf("hashPassword: %v", err)
	}
	h2, s2, it2, err := hashPassword("correct horse battery staple")
	if err != nil {
		t.Fatalf("hashPassword: %v", err)
	}
	if bytes.Equal(s1, s2) {
		t.Fatal("two hashes of the same password got the same salt")
	}
	if bytes.Equal(h1, h2) {
		t.Fatal("two hashes of the same password (different salts) produced the same hash")
	}
	if it1 != PBKDF2Iterations || it2 != PBKDF2Iterations {
		t.Fatalf("iterations = %d, %d, want %d", it1, it2, PBKDF2Iterations)
	}
	if len(s1) < 16 {
		t.Fatalf("salt is %d bytes, want at least 16", len(s1))
	}
}

func TestVerifyPassword(t *testing.T) {
	hash, salt, iterations, err := hashPassword("s3cret-passphrase")
	if err != nil {
		t.Fatalf("hashPassword: %v", err)
	}
	if !verifyPassword("s3cret-passphrase", hash, salt, iterations) {
		t.Fatal("correct password rejected")
	}
	if verifyPassword("wrong-passphrase", hash, salt, iterations) {
		t.Fatal("wrong password accepted")
	}
	if verifyPassword("", hash, salt, iterations) {
		t.Fatal("empty password accepted")
	}
}

// TestVerifyPasswordNearMiss checks that verifyPassword uses a full,
// constant-time comparison (crypto/subtle.ConstantTimeCompare) rather than a
// short-circuiting byte loop: a stored hash that differs from the correct
// one in only its last byte must still be rejected, not accidentally treated
// as "close enough".
func TestVerifyPasswordNearMiss(t *testing.T) {
	hash, salt, iterations, err := hashPassword("whatever")
	if err != nil {
		t.Fatalf("hashPassword: %v", err)
	}
	corrupted := append([]byte(nil), hash...)
	corrupted[len(corrupted)-1] ^= 0xFF
	if verifyPassword("whatever", corrupted, salt, iterations) {
		t.Fatal("a hash corrupted in its last byte matched")
	}
}
