package auth

import (
	"fmt"
	"strings"
	"time"

	"github.com/pquerna/otp"
	"github.com/pquerna/otp/totp"
)

// TOTPIssuer is shown in authenticator apps.
const TOTPIssuer = "Kadr"

// GenerateTOTP creates a new TOTP secret for account (the admin email).
func GenerateTOTP(account string) (secret, otpauthURL string, err error) {
	key, err := totp.Generate(totp.GenerateOpts{Issuer: TOTPIssuer, AccountName: account})
	if err != nil {
		return "", "", fmt.Errorf("auth: totp generate: %w", err)
	}
	return key.Secret(), key.URL(), nil
}

// ValidateTOTP checks a 6-digit code allowing ±1 period of clock skew.
func ValidateTOTP(code, secret string, now time.Time) bool {
	code = strings.ReplaceAll(strings.TrimSpace(code), " ", "")
	if len(code) != 6 {
		return false
	}
	ok, err := totp.ValidateCustom(code, secret, now, totp.ValidateOpts{
		Period: 30, Skew: 1, Digits: otp.DigitsSix, Algorithm: otp.AlgorithmSHA1,
	})
	return err == nil && ok
}
