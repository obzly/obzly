package sharedconfig

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// The control plane's document carries the Site's maintenance, and a Site
// applying it hands the silences over (an empty list when there are none,
// so deleted windows lose their silences).
func Test_Document_CarriesMaintenance_AndApplyHandsItToTheSite(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	sent := []MaintenanceSilence{{
		Key: "synthetics-maintenance:w:p:1", ProjectID: "p", Comment: "Synthetics maintenance: Weekly DB patching",
		CheckIDs: []string{"c1"}, StartsAt: now, EndsAt: now.Add(time.Hour),
	}}
	var applied [][]MaintenanceSilence
	SetMaintenance(func(siteID string) ([]MaintenanceSilence, error) {
		if siteID == "site-a" {
			return sent, nil
		}
		return nil, nil
	}, func(silences []MaintenanceSilence) error {
		applied = append(applied, silences)
		return nil
	})
	t.Cleanup(func() { SetMaintenance(nil, nil) })

	plain, version, err := (&payloadSource{}).payloadFor("site-a")
	require.NoError(t, err)
	var content payload
	require.NoError(t, json.Unmarshal(plain, &content))
	require.Len(t, content.Maintenance, 1)
	require.Equal(t, sent[0].Key, content.Maintenance[0].Key)
	require.True(t, sent[0].EndsAt.Equal(content.Maintenance[0].EndsAt))
	_, otherVersion, err := (&payloadSource{}).payloadFor("site-b")
	require.NoError(t, err)
	require.NotEqual(t, version, otherVersion, "the maintenance is part of the version")

	key, err := PublicKey()
	require.NoError(t, err)
	sealed, err := Seal(key, plain)
	require.NoError(t, err)
	_, err = apply(&document{TokenUse: TokenUse, Version: "maintenance-" + version, Sealed: sealed,
		IssuedAt: time.Now().Unix(), Expiry: time.Now().Add(time.Hour).Unix()}, time.Now().UTC())
	require.NoError(t, err)
	_, err = apply(testDocument(t, "maintenance-none-"+version, time.Now()), time.Now().UTC())
	require.NoError(t, err)

	require.Len(t, applied, 2)
	require.Len(t, applied[0], 1)
	require.Equal(t, sent[0].CheckIDs, applied[0][0].CheckIDs)
	require.Empty(t, applied[1])
}
