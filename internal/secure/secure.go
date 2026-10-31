// Package secure centralizes token generation, hashing and password helpers.
package secure

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"strings"

	"golang.org/x/crypto/bcrypt"
)

// RandomToken returns n bytes of crypto randomness, base64url encoded.
func RandomToken(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// SHA256Hex is used for session, reset, proxy and QR tokens at rest.
func SHA256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// HMACHex is used for registration codes at rest and CSRF derivation.
func HMACHex(key []byte, s string) string {
	m := hmac.New(sha256.New, key)
	_, _ = m.Write([]byte(s))
	return hex.EncodeToString(m.Sum(nil))
}

// Equal compares two strings without leaking timing.
func Equal(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

// HashPassword returns a bcrypt hash.
func HashPassword(pw string) (string, error) {
	b, err := bcrypt.GenerateFromPassword([]byte(pw), bcrypt.DefaultCost)
	return string(b), err
}

// CheckPassword verifies a bcrypt hash.
func CheckPassword(hash, pw string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(pw)) == nil
}

var dummyHash, _ = bcrypt.GenerateFromPassword([]byte("dummy-password-for-timing"), bcrypt.DefaultCost)

// CheckDummy burns comparable time when the account does not exist.
func CheckDummy(pw string) {
	_ = bcrypt.CompareHashAndPassword(dummyHash, []byte(pw))
}

const codeAlphabet = "ABCDEFGHJKMNPQRSTUVWXYZ23456789"

// NewRegistrationCode returns the normalized 12-char code and its formatted
// display form XXXX-XXXX-XXXX.
func NewRegistrationCode() (normalized, display string, err error) {
	var b strings.Builder
	max := big.NewInt(int64(len(codeAlphabet)))
	for i := 0; i < 12; i++ {
		n, err := rand.Int(rand.Reader, max)
		if err != nil {
			return "", "", err
		}
		b.WriteByte(codeAlphabet[n.Int64()])
	}
	normalized = b.String()
	return normalized, FormatCode(normalized), nil
}

// FormatCode inserts hyphens every four characters.
func FormatCode(norm string) string {
	var parts []string
	for i := 0; i < len(norm); i += 4 {
		end := i + 4
		if end > len(norm) {
			end = len(norm)
		}
		parts = append(parts, norm[i:end])
	}
	return strings.Join(parts, "-")
}

// NormalizeCode lowers ambiguity: removes spaces/hyphens and upper-cases.
func NormalizeCode(raw string) string {
	s := strings.ToUpper(strings.TrimSpace(raw))
	s = strings.NewReplacer(" ", "", "-", "", "\u3000", "", "\u2014", "", "\u2013", "").Replace(s)
	return s
}

// CodeHint returns the first four characters used for list identification.
func CodeHint(norm string) string {
	if len(norm) < 4 {
		return norm
	}
	return norm[:4]
}

// ResetCode returns a 6-digit numeric code.
func ResetCode() (string, error) {
	n, err := rand.Int(rand.Reader, big.NewInt(1000000))
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%06d", n.Int64()), nil
}

// ParseInt is a tiny helper for positive ids.
func ParseInt(s string) (int64, error) {
	var n int64
	if s == "" {
		return 0, errors.New("空值")
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0, errors.New("非法数字")
		}
		n = n*10 + int64(r-'0')
		if n > 1<<53 {
			return 0, errors.New("数字过大")
		}
	}
	return n, nil
}
