// Package auth provides password hashing, random tokens and CSRF primitives.
package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"
)

// Argon2id parameters (OWASP minimum profile m=19 MiB, t=2, p=1). Memory is the constraint on a
// 256 MB host, so hashing is serialised by a small semaphore.
const (
	argonMemory  = 19 * 1024
	argonTime    = 2
	argonThreads = 1
	argonKeyLen  = 32
	saltLen      = 16
	// MinPasswordLen is the minimum accepted password length (legacy rule).
	MinPasswordLen = 8
)

var hashSlots = make(chan struct{}, 2)

// HashPassword returns a self-describing argon2id hash.
func HashPassword(password string) (string, error) {
	hashSlots <- struct{}{}
	defer func() { <-hashSlots }()
	salt := make([]byte, saltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key := argon2.IDKey([]byte(password), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s", argon2.Version, argonMemory, argonTime, argonThreads,
		base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(key)), nil
}

// VerifyPassword checks password against an encoded hash in constant time.
func VerifyPassword(encoded, password string) bool {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false
	}
	var v, m, t, p int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &v); err != nil || v != argon2.Version {
		return false
	}
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &m, &t, &p); err != nil {
		return false
	}
	// Refuse absurd parameters from a corrupted row: they would exhaust memory.
	if m < 8 || m > 128*1024 || t < 1 || t > 10 || p < 1 || p > 4 {
		return false
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return false
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil || len(want) == 0 {
		return false
	}
	hashSlots <- struct{}{}
	defer func() { <-hashSlots }()
	got := argon2.IDKey([]byte(password), salt, uint32(t), uint32(m), uint8(p), uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1
}

// DummyVerify burns comparable time for unknown accounts, flattening user-enumeration timing.
func DummyVerify() {
	VerifyPassword("$argon2id$v=19$m=19456,t=2,p=1$c2FsdHNhbHRzYWx0c2FsdA$AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", "x")
}

// NewToken returns a random URL-safe token and its storage hash. Only the hash is persisted.
func NewToken() (raw, hash string, err error) {
	b := make([]byte, 32)
	if _, err = rand.Read(b); err != nil {
		return "", "", err
	}
	raw = base64.RawURLEncoding.EncodeToString(b)
	return raw, HashToken(raw), nil
}

// HashToken is the storage form of a raw token.
func HashToken(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

// RandomCode returns n characters drawn from alphabet using crypto/rand without modulo bias.
func RandomCode(alphabet string, n int) (string, error) {
	if len(alphabet) == 0 || len(alphabet) > 256 {
		return "", errors.New("bad alphabet")
	}
	limit := 256 - (256 % len(alphabet))
	out := make([]byte, 0, n)
	buf := make([]byte, n*2)
	for len(out) < n {
		if _, err := rand.Read(buf); err != nil {
			return "", err
		}
		for _, b := range buf {
			if int(b) >= limit {
				continue
			}
			out = append(out, alphabet[int(b)%len(alphabet)])
			if len(out) == n {
				break
			}
		}
	}
	return string(out), nil
}

// CSRF derives the form token that pairs with the per-browser csrf cookie value.
func CSRF(secret, cookieValue string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte("csrf\n" + cookieValue))
	return hex.EncodeToString(mac.Sum(nil))
}

// CheckCSRF validates a submitted token in constant time.
func CheckCSRF(secret, cookieValue, submitted string) bool {
	if cookieValue == "" || submitted == "" {
		return false
	}
	return hmac.Equal([]byte(CSRF(secret, cookieValue)), []byte(submitted))
}

// Sign computes the webhook signature: hex(HMAC-SHA256(secret, timestamp + "\n" + body)).
func Sign(secret, timestamp string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(timestamp + "\n"))
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}
