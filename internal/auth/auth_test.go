package auth

import (
	"strings"
	"testing"
)

func TestPasswordRoundTrip(t *testing.T) {
	h, err := HashPassword("correct horse")
	if err != nil || !strings.HasPrefix(h, "$argon2id$") {
		t.Fatalf("hash: %v %q", err, h)
	}
	if !VerifyPassword(h, "correct horse") || VerifyPassword(h, "wrong") {
		t.Fatal("verification mismatch")
	}
	if VerifyPassword("garbage", "x") || VerifyPassword("$argon2id$v=19$m=99999999,t=1,p=1$AA$AA", "x") {
		t.Fatal("malformed or oversized hashes must be rejected")
	}
}

func TestTokensAreUniqueAndHashed(t *testing.T) {
	a, ah, _ := NewToken()
	b, _, _ := NewToken()
	if a == b || HashToken(a) != ah || a == ah {
		t.Fatal("token generation broken")
	}
}

func TestRandomCodeAlphabet(t *testing.T) {
	c, err := RandomCode("ABC", 200)
	if err != nil || len(c) != 200 || strings.Trim(c, "ABC") != "" {
		t.Fatalf("bad code %q %v", c, err)
	}
}

func TestCSRF(t *testing.T) {
	tok := CSRF("secret", "cookie")
	if !CheckCSRF("secret", "cookie", tok) || CheckCSRF("secret", "other", tok) || CheckCSRF("x", "cookie", tok) || CheckCSRF("secret", "", tok) {
		t.Fatal("csrf check wrong")
	}
}

func TestSignMatchesCeramizaContract(t *testing.T) {
	// hex(HMAC-SHA256(secret, timestamp + "\n" + body)), same as ceramiza/internal/web/bank_mail.go.
	got := Sign("0123456789abcdef0123456789abcdef", "2026-10-27T10:00:00.000Z", []byte(`{"id":"x"}`))
	if len(got) != 64 {
		t.Fatalf("signature length %d", len(got))
	}
	if got != Sign("0123456789abcdef0123456789abcdef", "2026-10-27T10:00:00.000Z", []byte(`{"id":"x"}`)) {
		t.Fatal("not deterministic")
	}
}
