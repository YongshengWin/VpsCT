package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base32"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"net/url"
	"strings"
	"time"
)

// TOTP parameters (RFC 6238 defaults, what Google Authenticator / 1Password
// / Authy expect from an otpauth URL without explicit parameters).
const (
	totpPeriod = 30
	totpDigits = 6
	// totpSkew accepts one step either side of "now" to absorb clock drift.
	totpSkew = 1
)

var b32 = base32.StdEncoding.WithPadding(base32.NoPadding)

// NewTOTPSecret returns a fresh 160-bit secret in base32 (no padding).
func NewTOTPSecret() string {
	b := make([]byte, 20)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return b32.EncodeToString(b)
}

// OTPAuthURL builds the otpauth:// URI encoded into the enrolment QR code.
func OTPAuthURL(issuer, account, secret string) string {
	label := url.PathEscape(issuer + ":" + account)
	q := url.Values{}
	q.Set("secret", secret)
	q.Set("issuer", issuer)
	q.Set("algorithm", "SHA1")
	q.Set("digits", fmt.Sprint(totpDigits))
	q.Set("period", fmt.Sprint(totpPeriod))
	return "otpauth://totp/" + label + "?" + q.Encode()
}

// TOTPStep returns the time step counter for t.
func TOTPStep(t time.Time) int64 { return t.Unix() / totpPeriod }

// TOTPCode computes the code for a given step.
func TOTPCode(secret string, step int64) (string, error) {
	key, err := b32.DecodeString(strings.ToUpper(strings.TrimSpace(secret)))
	if err != nil {
		return "", err
	}
	var msg [8]byte
	binary.BigEndian.PutUint64(msg[:], uint64(step))
	mac := hmac.New(sha1.New, key)
	mac.Write(msg[:])
	sum := mac.Sum(nil)
	off := sum[len(sum)-1] & 0x0f
	v := binary.BigEndian.Uint32(sum[off:off+4]) & 0x7fffffff
	mod := uint32(1)
	for i := 0; i < totpDigits; i++ {
		mod *= 10
	}
	return fmt.Sprintf("%0*d", totpDigits, v%mod), nil
}

// VerifyTOTP checks code against secret around now. lastStep is the most
// recent step already accepted for this secret; matching steps at or before
// it are rejected so a captured code cannot be replayed within the window.
// On success it returns the matched step to persist as the new lastStep.
func VerifyTOTP(secret, code string, now time.Time, lastStep int64) (int64, bool) {
	code = strings.ReplaceAll(strings.TrimSpace(code), " ", "")
	if len(code) != totpDigits {
		return 0, false
	}
	cur := TOTPStep(now)
	matched := int64(0)
	ok := false
	for d := int64(-totpSkew); d <= totpSkew; d++ {
		step := cur + d
		want, err := TOTPCode(secret, step)
		if err != nil {
			return 0, false
		}
		// evaluate every candidate to keep timing independent of which matches
		if subtle.ConstantTimeCompare([]byte(want), []byte(code)) == 1 && step > lastStep {
			ok = true
			matched = step
		}
	}
	return matched, ok
}

// ---- recovery codes ----

const recoveryAlphabet = "abcdefghjkmnpqrstuvwxyz23456789"

// NewRecoveryCodes returns n one-time codes (xxxx-xxxx) and their hashes.
func NewRecoveryCodes(n int) (plain []string, hashes []string) {
	for i := 0; i < n; i++ {
		b := make([]byte, 8)
		if _, err := rand.Read(b); err != nil {
			panic(err)
		}
		var sb strings.Builder
		for j, x := range b {
			if j == 4 {
				sb.WriteByte('-')
			}
			sb.WriteByte(recoveryAlphabet[int(x)%len(recoveryAlphabet)])
		}
		c := sb.String()
		plain = append(plain, c)
		hashes = append(hashes, HashRecoveryCode(c))
	}
	return plain, hashes
}

// HashRecoveryCode normalises (case, separators) and hashes a recovery code.
func HashRecoveryCode(code string) string {
	norm := strings.ToLower(strings.NewReplacer("-", "", " ", "").Replace(strings.TrimSpace(code)))
	sum := sha256.Sum256([]byte("recovery:" + norm))
	return hex.EncodeToString(sum[:])
}

// UseRecoveryCode returns the hash list with the matching code removed, or
// ok=false when the code is not one of the unused codes.
func UseRecoveryCode(hashes []string, code string) (remaining []string, ok bool) {
	h := HashRecoveryCode(code)
	for i, x := range hashes {
		if subtle.ConstantTimeCompare([]byte(x), []byte(h)) == 1 {
			remaining = append(remaining, hashes[:i]...)
			remaining = append(remaining, hashes[i+1:]...)
			return remaining, true
		}
	}
	return hashes, false
}
