// Package auth provides password hashing, random tokens and hashing helpers
// shared by sessions, subscriptions and agent enrolment.
package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"strings"

	"golang.org/x/crypto/argon2"
)

const (
	argonTime    = 2
	argonMemory  = 64 * 1024
	argonThreads = 2
	argonKeyLen  = 32
)

// HashPassword returns an argon2id PHC string.
func HashPassword(password string) (string, error) {
	if len(password) > 512 {
		return "", errors.New("password too long")
	}
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key := argon2.IDKey([]byte(password), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	return fmt.Sprintf("$argon2id$v=19$m=%d,t=%d,p=%d$%s$%s", argonMemory, argonTime, argonThreads,
		base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(key)), nil
}

// VerifyPassword checks password against a PHC string produced by HashPassword.
func VerifyPassword(encoded, password string) bool {
	if len(password) > 512 || len(encoded) > 1024 {
		return false
	}
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false
	}
	var mem, tm uint32
	var par uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &mem, &tm, &par); err != nil {
		return false
	}
	if parts[2] != "v=19" || mem < 8 || mem > 65536 || tm < 1 || tm > 4 || par < 1 || par > 4 {
		return false
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return false
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil {
		return false
	}
	if len(salt) < 8 || len(salt) > 64 || len(want) != 32 {
		return false
	}
	got := argon2.IDKey([]byte(password), salt, tm, mem, par, uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1
}

// RandomToken returns n random bytes encoded as URL-safe base64.
func RandomToken(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

// SubscriptionTokenBytes is 192 random bytes → 256 chars of raw URL-safe base64.
const SubscriptionTokenBytes = 192

// NewSubscriptionToken returns a 256-character subscription capability token.
func NewSubscriptionToken() string {
	return RandomToken(SubscriptionTokenBytes)
}

// TokenHint is the prefix shown in the UI.
func TokenHint(token string) string {
	if len(token) <= 6 {
		return token
	}
	return token[:6]
}

const shortAlphabet = "abcdefghijkmnpqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789"

// ShortCode returns a human-friendly random code of length n.
func ShortCode(n int) string {
	if n < 24 {
		n = 24
	}
	out := make([]byte, n)
	for i := range out {
		v, err := rand.Int(rand.Reader, big.NewInt(int64(len(shortAlphabet))))
		if err != nil {
			panic(err)
		}
		out[i] = shortAlphabet[v.Int64()]
	}
	return string(out)
}

// HashToken hashes an opaque token for storage (sha256 hex).
func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// ErrInvalidCredentials is returned by login helpers.
var ErrInvalidCredentials = errors.New("invalid credentials")
