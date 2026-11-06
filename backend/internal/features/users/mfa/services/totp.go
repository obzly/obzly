// Package mfa_services implements TOTP MFA: mandatory for Admin-role users,
// optional for Members. pquerna/otp alone would accept the same code repeatedly
// within its window, so verifyAndFindStep rejects any step at or before the
// last consumed one.
package mfa_services

import (
	"crypto/subtle"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/pquerna/otp"
	"github.com/pquerna/otp/totp"
)

const (
	totpPeriodSeconds = 30
	// totpSkewSteps tolerates one period of clock drift either way (pquerna/otp's default).
	totpSkewSteps = 1
)

var (
	errInvalidCode = errors.New("invalid MFA code")
	errCodeReused  = errors.New("this MFA code has already been used - wait for the next one")
)

var validateOpts = totp.ValidateOpts{
	Period:    totpPeriodSeconds,
	Skew:      totpSkewSteps,
	Digits:    otp.DigitsSix,
	Algorithm: otp.AlgorithmSHA1,
}

// generateSecret creates a new TOTP key for accountEmail, Google-
// Authenticator-compatible (SHA1/6-digit/30s, pquerna/otp's defaults).
func generateSecret(accountEmail string) (*otp.Key, error) {
	key, err := totp.Generate(totp.GenerateOpts{
		Issuer:      "Obzly",
		AccountName: accountEmail,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to generate TOTP secret: %w", err)
	}
	return key, nil
}

// currentStep is the TOTP time-step (RFC 6238 "T") for now.
func currentStep() int64 {
	return int64(math.Floor(float64(time.Now().UTC().Unix()) / float64(totpPeriodSeconds)))
}

// verifyAndFindStep returns the matched time-step, which callers must persist
// before treating the code as consumed. lastUsedStep 0 means none consumed yet.
func verifyAndFindStep(secret, code string, lastUsedStep int64) (int64, error) {
	now := time.Now().UTC()
	step := currentStep()

	for offset := -int64(totpSkewSteps); offset <= int64(totpSkewSteps); offset++ {
		candidateStep := step + offset
		if candidateStep <= lastUsedStep {
			// Would be a replay even if it matched.
			continue
		}

		candidateTime := now.Add(time.Duration(offset) * totpPeriodSeconds * time.Second)
		candidateCode, err := totp.GenerateCodeCustom(secret, candidateTime, validateOpts)
		if err != nil {
			return 0, fmt.Errorf("failed to compute candidate TOTP code: %w", err)
		}

		if subtle.ConstantTimeCompare([]byte(candidateCode), []byte(code)) == 1 {
			return candidateStep, nil
		}
	}

	// Tell a replayed step apart from a wrong code.
	for offset := -int64(totpSkewSteps); offset <= int64(totpSkewSteps); offset++ {
		candidateStep := step + offset
		if candidateStep > lastUsedStep {
			continue
		}

		candidateTime := now.Add(time.Duration(offset) * totpPeriodSeconds * time.Second)
		candidateCode, err := totp.GenerateCodeCustom(secret, candidateTime, validateOpts)
		if err == nil && subtle.ConstantTimeCompare([]byte(candidateCode), []byte(code)) == 1 {
			return 0, errCodeReused
		}
	}

	return 0, errInvalidCode
}
