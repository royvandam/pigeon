// Package token creates the random tokens that identify sessions and share
// links and protect forms against cross-site requests.
package token

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
)

// Length is the length of every token: 32 random bytes in unpadded base64url.
const Length = 43

// New returns a random token.
func New() (string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b[:]), nil
}

// Hash returns the hex-encoded SHA-256 hash of a token, which is what the
// database stores in place of the token itself.
func Hash(token string) string {
	hash := sha256.Sum256([]byte(token))
	return hex.EncodeToString(hash[:])
}
