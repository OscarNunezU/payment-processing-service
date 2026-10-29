package domain

import (
	"crypto/rand"
	"crypto/sha256"
	"strings"
)

// APIKeyPrefix starts every secret API key. There is no live mode: every key
// works against the simulated acquirer.
const APIKeyPrefix = "sk_test_"

// apiKeyMinLen and apiKeyMaxLen bound what is worth looking up: anything
// outside them cannot be a key.
const (
	apiKeyMinLen = len(APIKeyPrefix) + 12
	apiKeyMaxLen = 128
)

// NewAPIKey returns a new secret key: the prefix and 128 random bits. It is
// shown once; the service keeps only its hash (RFC 0001, section 14).
func NewAPIKey() string { return APIKeyPrefix + rand.Text() }

// HashAPIKey returns the SHA-256 the service stores for secret, or false when
// secret is not shaped like a key. A random key has enough entropy that a fast
// hash is safe; a slow one, such as bcrypt, is for passwords people choose.
func HashAPIKey(secret string) ([sha256.Size]byte, bool) {
	if !strings.HasPrefix(secret, APIKeyPrefix) || len(secret) < apiKeyMinLen || len(secret) > apiKeyMaxLen {
		return [sha256.Size]byte{}, false
	}
	return sha256.Sum256([]byte(secret)), true
}

// APIKeyHint returns the first characters of secret, which are stored next
// to the hash so a key can be recognized without revealing it. secret must be
// shaped like a key, as HashAPIKey checks.
func APIKeyHint(secret string) string { return secret[:len(APIKeyPrefix)+4] }
