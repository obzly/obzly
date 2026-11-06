package users_services

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func Test_checkAssertionTimes(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	at := func(d time.Duration) int64 { return now.Add(d).Unix() }

	cases := []struct {
		name    string
		claims  centralAssertionClaims
		wantErr bool
	}{
		{"normal 60s assertion", centralAssertionClaims{IssuedAt: at(0), NotBefore: at(-5 * time.Second), Expiry: at(60 * time.Second)}, false},
		{"small clock skew tolerated", centralAssertionClaims{IssuedAt: at(90 * time.Second), Expiry: at(150 * time.Second)}, false},
		{"missing iat rejected", centralAssertionClaims{Expiry: at(60 * time.Second)}, true},
		{"iat far in the future rejected", centralAssertionClaims{IssuedAt: at(10 * time.Minute), Expiry: at(11 * time.Minute)}, true},
		{"nbf in the future rejected", centralAssertionClaims{IssuedAt: at(0), NotBefore: at(10 * time.Minute), Expiry: at(60 * time.Second)}, true},
		{"over-long lifetime rejected", centralAssertionClaims{IssuedAt: at(0), Expiry: at(24 * time.Hour)}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := checkAssertionTimes(&tc.claims, now)
			if tc.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}
