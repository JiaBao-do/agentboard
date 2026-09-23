package agentboard

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/binary"
	"fmt"
)

// PBKDF2Iterations is the default PBKDF2-HMAC-SHA256 work factor applied to
// new password hashes. 600,000 is OWASP's current minimum recommendation
// for PBKDF2-HMAC-SHA256 (OWASP Password Storage Cheat Sheet, revised
// December 2022 based on GPU cracking benchmarks; still the current published
// guidance): https://cheatsheetseries.owasp.org/cheatsheets/Password_Storage_Cheat_Sheet.html
//
// It is deliberately expensive - on the order of 100ms on ordinary server
// hardware - to make offline brute force of a stolen data file costly. That
// cost is why Register and Authenticate run it outside the Board's mutex
// (see board.go, auth.go): holding the single board lock for ~100ms per
// login would serialize every other request behind it.
const PBKDF2Iterations = 600_000

const (
	saltLen = 16 // bytes; at least the OWASP/NIST minimum for a password salt
	keyLen  = 32 // bytes; SHA-256's output size, plenty for a derived key
)

// pbkdf2SHA256 derives a keyLen-byte key from password and salt using
// PBKDF2-HMAC-SHA256 (RFC 8018 section 5.2), built only from the standard
// library (crypto/hmac, crypto/sha256, encoding/binary). agentboard ships
// zero third-party dependencies, so this cannot use golang.org/x/crypto/pbkdf2.
func pbkdf2SHA256(password string, salt []byte, iterations, keyLen int) []byte {
	prf := hmac.New(sha256.New, []byte(password))
	hLen := prf.Size()
	numBlocks := (keyLen + hLen - 1) / hLen
	dk := make([]byte, 0, numBlocks*hLen)
	be := make([]byte, 4)
	for block := 1; block <= numBlocks; block++ {
		binary.BigEndian.PutUint32(be, uint32(block)) // block index, always small and positive
		prf.Reset()
		prf.Write(salt)
		prf.Write(be)
		u := prf.Sum(nil)
		t := make([]byte, len(u))
		copy(t, u)
		for i := 1; i < iterations; i++ {
			prf.Reset()
			prf.Write(u)
			u = prf.Sum(nil)
			for j := range t {
				t[j] ^= u[j]
			}
		}
		dk = append(dk, t...)
	}
	return dk[:keyLen]
}

// hashPassword returns a fresh random salt and the PBKDF2-HMAC-SHA256 hash
// of password at PBKDF2Iterations, ready to store on a model.User. It never
// returns anything the plaintext password can be recovered from.
func hashPassword(password string) (hash, salt []byte, iterations int, err error) {
	salt = make([]byte, saltLen)
	if _, err := rand.Read(salt); err != nil {
		return nil, nil, 0, fmt.Errorf("agentboard: generating password salt: %w", err)
	}
	return pbkdf2SHA256(password, salt, PBKDF2Iterations, keyLen), salt, PBKDF2Iterations, nil
}

// verifyPassword reports whether password matches hash under the given salt
// and iteration count. The comparison is constant-time (crypto/subtle) so
// that timing cannot reveal how many leading bytes of the hash matched.
func verifyPassword(password string, hash, salt []byte, iterations int) bool {
	got := pbkdf2SHA256(password, salt, iterations, len(hash))
	return subtle.ConstantTimeCompare(got, hash) == 1
}
