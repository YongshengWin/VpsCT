package auth

import (
	"strings"
	"testing"
	"time"
)

// RFC 6238 Appendix B vectors (SHA1, secret "12345678901234567890"), last 6
// of the 8-digit reference codes.
func TestTOTPCodeVectors(t *testing.T) {
	secret := "GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ" // gitleaks:allow -- RFC 6238 Appendix B public test vector
	cases := map[int64]string{59: "287082", 1111111109: "081804", 1111111111: "050471", 1234567890: "005924", 2000000000: "279037"}
	for ts, want := range cases {
		got, err := TOTPCode(secret, TOTPStep(time.Unix(ts, 0)))
		if err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Errorf("t=%d: got %s want %s", ts, got, want)
		}
	}
}

func TestVerifyTOTP(t *testing.T) {
	secret := NewTOTPSecret()
	now := time.Unix(1_700_000_000, 0)
	code, _ := TOTPCode(secret, TOTPStep(now))
	step, ok := VerifyTOTP(secret, code, now, 0)
	if !ok || step != TOTPStep(now) {
		t.Fatalf("current code rejected")
	}
	// replay of the same step is rejected
	if _, ok := VerifyTOTP(secret, code, now, step); ok {
		t.Fatalf("replayed code accepted")
	}
	// previous step still accepted (clock skew), two steps back is not
	prev, _ := TOTPCode(secret, TOTPStep(now)-1)
	if _, ok := VerifyTOTP(secret, prev, now, 0); !ok {
		t.Fatalf("previous-step code rejected")
	}
	old, _ := TOTPCode(secret, TOTPStep(now)-2)
	if _, ok := VerifyTOTP(secret, old, now, 0); ok {
		t.Fatalf("stale code accepted")
	}
	if _, ok := VerifyTOTP(secret, "12345", now, 0); ok {
		t.Fatalf("short code accepted")
	}
}

func TestRecoveryCodes(t *testing.T) {
	plain, hashes := NewRecoveryCodes(8)
	if len(plain) != 8 || len(hashes) != 8 {
		t.Fatal("wrong count")
	}
	if !strings.Contains(plain[0], "-") || len(plain[0]) != 9 {
		t.Fatalf("unexpected format %q", plain[0])
	}
	rest, ok := UseRecoveryCode(hashes, strings.ToUpper(plain[3]))
	if !ok || len(rest) != 7 {
		t.Fatalf("code not accepted (case-insensitive)")
	}
	if _, ok := UseRecoveryCode(rest, plain[3]); ok {
		t.Fatalf("code reused")
	}
	if _, ok := UseRecoveryCode(rest, "zzzz-zzzz"); ok {
		t.Fatalf("bogus code accepted")
	}
}

func TestOTPAuthURL(t *testing.T) {
	u := OTPAuthURL("VpsCT", "admin", "ABC")
	if !strings.HasPrefix(u, "otpauth://totp/VpsCT:admin?") || !strings.Contains(u, "secret=ABC") || !strings.Contains(u, "issuer=VpsCT") {
		t.Fatalf("bad url %s", u)
	}
}
