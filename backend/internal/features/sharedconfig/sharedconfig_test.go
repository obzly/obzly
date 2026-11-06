package sharedconfig

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/nacl/box"

	"obzly/internal/storage"
)

func Test_Seal_OnlyThisDeploymentsKeyOpensIt(t *testing.T) {
	key, err := PublicKey()
	require.NoError(t, err)
	other, _, err := box.GenerateKey(rand.Reader)
	require.NoError(t, err)

	sealed, err := Seal(key, []byte("channel secrets"))
	require.NoError(t, err)
	opened, err := Open(sealed)
	toOther, err2 := Seal(base64.StdEncoding.EncodeToString(other[:]), []byte("channel secrets"))
	require.NoError(t, err2)
	_, otherErr := Open(toOther)

	require.NoError(t, err)
	require.Equal(t, "channel secrets", string(opened))
	require.Error(t, otherErr)
}

func Test_ScopeForSite(t *testing.T) {
	scope := Scope{
		Sites:    []string{"a"},
		Projects: []ProjectRef{{SiteID: "b", ProjectID: "p1"}, {SiteID: "b", ProjectID: "p2"}},
	}

	all, projects := scope.ForSite("a")
	require.True(t, all)
	require.Nil(t, projects)
	all, projects = scope.ForSite("b")
	require.False(t, all)
	require.Equal(t, []string{"p1", "p2"}, projects)
	all, projects = scope.ForSite("c")
	require.False(t, all)
	require.Empty(t, projects)
	all, _ = Scope{AllSites: true}.ForSite("c")
	require.True(t, all)
}

// A Site applies what its control plane sends under the same channel ids
// (a channel it had moved to central becomes the central one), and drops
// central channels that are no longer sent.
func Test_Apply_UpsertsCentralChannelsAndRemovesDroppedOnes(t *testing.T) {
	moved, added := uuid.New(), uuid.New()
	cleanChannels(t, moved, added)
	require.NoError(t, storage.GetDb().Exec(`INSERT INTO notification_channels
		(id, name, kind, config_encrypted, source, project_ids) VALUES (?, 'moved', 'slack', 'x', 'local', '["p1"]')`, moved).Error)

	first := testDocument(t, "v1", time.Now().Add(-time.Minute), SyncedChannel{
		ID:          moved,
		Name:        "ops",
		Kind:        "slack",
		Config:      json.RawMessage(`{"url":"https://hooks.example.com/a"}`),
		AllProjects: true,
	}, SyncedChannel{
		ID:         added,
		Name:       "pager",
		Kind:       "pagerduty",
		Config:     json.RawMessage(`{"routingKey":"rk"}`),
		ProjectIDs: []string{"p2"},
	})
	version, err := apply(first, time.Now().UTC())
	require.NoError(t, err)
	require.Equal(t, "v1", version)

	rows := channelRows(t, moved, added)
	require.Len(t, rows, 2)
	require.Equal(t, SourceCentral, rows[moved].Source)
	require.True(t, rows[moved].AllProjects)
	require.Equal(t, "ops", rows[moved].Name)
	plain, err := secrets.Decrypt(rows[moved].ConfigEncrypted)
	require.NoError(t, err)
	require.JSONEq(t, `{"url":"https://hooks.example.com/a"}`, plain)
	require.Equal(t, `["p2"]`, rows[added].ProjectIDs)

	second := testDocument(t, "v2", time.Now(), SyncedChannel{
		ID:         added,
		Name:       "pager",
		Kind:       "pagerduty",
		Config:     json.RawMessage(`{"routingKey":"rk"}`),
		ProjectIDs: []string{"p2"},
	})
	_, err = apply(second, time.Now().UTC())
	require.NoError(t, err)

	rows = channelRows(t, moved, added)
	require.Len(t, rows, 1)
	require.Contains(t, rows, added)
	state, err := AppliedState()
	require.NoError(t, err)
	require.Equal(t, "v2", state.Version)
}

func Test_Adopt_StoresTheSitesChannelsCentrallyUnderTheSameIDs(t *testing.T) {
	require.NoError(t, storage.GetDb().Exec("DELETE FROM shared_channels").Error)
	t.Cleanup(func() { storage.GetDb().Exec("DELETE FROM shared_channels") })
	everywhere, someProjects := uuid.New(), uuid.New()
	key, err := PublicKey()
	require.NoError(t, err)
	plain, err := json.Marshal(payload{Channels: []SyncedChannel{
		{
			ID:          everywhere,
			Name:        "ops",
			Kind:        "slack",
			Config:      json.RawMessage(`{"url":"https://hooks.example.com/a"}`),
			AllProjects: true,
		},
		{
			ID:         someProjects,
			Name:       "mail",
			Kind:       "email",
			Config:     json.RawMessage(`{"to":["a@example.com"]}`),
			ProjectIDs: []string{"p1"},
		},
	}})
	require.NoError(t, err)
	sealed, err := Seal(key, plain)
	require.NoError(t, err)
	siteID := createTestSite(t, "http://127.0.0.1:1", "").ID

	result, err := Adopt(siteID, sealed, uuid.New())
	again, againErr := Adopt(siteID, sealed, uuid.New())

	require.NoError(t, err)
	require.NoError(t, againErr, "this Site's own channels can be moved again")
	require.ElementsMatch(t, []string{"ops", "mail"}, again.Channels)
	require.ElementsMatch(t, []string{"ops", "mail"}, result.Channels)
	channels, err := SharedChannels()
	require.NoError(t, err)
	byID := map[uuid.UUID]*SharedChannel{}
	for _, channel := range channels {
		byID[channel.ID] = channel
	}
	require.Equal(t, Scope{Sites: []string{siteID}}, byID[everywhere].Scope)
	require.Equal(t, Scope{Projects: []ProjectRef{{SiteID: siteID, ProjectID: "p1"}}}, byID[someProjects].Scope)
	_, version, err := (&payloadSource{}).payloadFor(siteID)
	require.NoError(t, err)
	require.NotEmpty(t, version)
}

func Test_Adopt_WhenAMonitorOnAnotherSiteUsesTheChannel_RefusesToReplaceIt(t *testing.T) {
	require.NoError(t, storage.GetDb().Exec("DELETE FROM shared_channels").Error)
	t.Cleanup(func() { storage.GetDb().Exec("DELETE FROM shared_channels") })
	channelID := uuid.New()
	key, err := PublicKey()
	require.NoError(t, err)
	plain, err := json.Marshal(payload{Channels: []SyncedChannel{{
		ID: channelID, Name: "pager", Kind: "slack",
		Config: json.RawMessage(`{"url":"https://hooks.example.com/p"}`), AllProjects: true,
	}}})
	require.NoError(t, err)
	sealed, err := Seal(key, plain)
	require.NoError(t, err)
	siteID := createTestSite(t, "http://127.0.0.1:1", "").ID
	otherSiteID := createTestSite(t, "http://127.0.0.1:2", "").ID
	_, err = Adopt(siteID, sealed, uuid.New())
	require.NoError(t, err)
	destinationID, checkID := uuid.New(), uuid.New()
	db := storage.GetDb()
	require.NoError(t, db.Exec(`INSERT INTO synthetic_destinations (id, name, site_id, project_id, otlp_url,
		api_key_encrypted) VALUES (?, ?, ?, 'p1', 'https://site.example.com', 'x')`,
		destinationID, "dest-"+destinationID.String(), otherSiteID).Error)
	require.NoError(t, db.Exec(`INSERT INTO synthetic_checks (id, name, type, destination_id, definition, alerts)
		VALUES (?, ?, 'http', ?, '{}', ?::jsonb)`,
		checkID, "check-"+checkID.String(), destinationID, `{"channelIds":["`+channelID.String()+`"]}`).Error)
	t.Cleanup(func() {
		db.Exec("DELETE FROM synthetic_checks WHERE id = ?", checkID)
		db.Exec("DELETE FROM synthetic_destinations WHERE id = ?", destinationID)
	})

	_, err = Adopt(siteID, sealed, uuid.New())

	require.ErrorIs(t, err, ErrChannelIDTaken)
}

func Test_ImportEnvSMTP_RunsOnceThenTheDatabaseWins(t *testing.T) {
	require.NoError(t, DeleteSMTP(SourceLocal))
	t.Cleanup(func() { _ = DeleteSMTP(SourceLocal) })
	t.Setenv("ALERT_SMTP_HOST", "smtp.example.com")
	t.Setenv("ALERT_SMTP_FROM", "alerts@example.com")
	t.Setenv("ALERT_SMTP_PASSWORD", "env-password")
	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	ImportEnvSMTP(SourceLocal, log)
	t.Setenv("ALERT_SMTP_HOST", "other.example.com")
	ImportEnvSMTP(SourceLocal, log)

	stored, err := StoredSMTP(SourceLocal)
	require.NoError(t, err)
	require.Equal(t, "smtp.example.com", stored.Host)
	require.Equal(t, 587, stored.Port)
	require.Equal(t, "starttls", stored.TLS)
	require.NotContains(t, stored.PasswordEncrypted, "env-password")
}

func Test_SaveSMTP_RefusesPlaintextAndKeepsThePasswordWhenLeftEmpty(t *testing.T) {
	require.NoError(t, DeleteSMTP(SourceLocal))
	t.Cleanup(func() { _ = DeleteSMTP(SourceLocal) })

	require.Error(t, SaveSMTP(SourceLocal, SMTP{Host: "smtp.example.com", From: "a@example.com", TLS: "none"}))
	require.NoError(
		t,
		SaveSMTP(SourceLocal, SMTP{Host: "smtp.example.com", From: "a@example.com", Username: "u", Password: "p"}),
	)
	require.NoError(t, SaveSMTP(SourceLocal, SMTP{Host: "smtp2.example.com", From: "a@example.com", Username: "u"}))

	forgetSMTP()
	current, err := CurrentSMTP()
	require.NoError(t, err)
	require.Equal(t, "smtp2.example.com", current.Host)
	require.Equal(t, "p", current.Password)
}

type testChannel struct {
	ID              uuid.UUID `gorm:"column:id"`
	Name            string    `gorm:"column:name"`
	Source          string    `gorm:"column:source"`
	AllProjects     bool      `gorm:"column:all_projects"`
	ProjectIDs      string    `gorm:"column:project_ids"`
	ConfigEncrypted string    `gorm:"column:config_encrypted"`
}

func testDocument(t *testing.T, version string, issued time.Time, channels ...SyncedChannel) *document {
	t.Helper()
	key, err := PublicKey()
	require.NoError(t, err)
	plain, err := json.Marshal(payload{Channels: channels})
	require.NoError(t, err)
	sealed, err := Seal(key, plain)
	require.NoError(t, err)
	return &document{
		TokenUse: TokenUse,
		Version:  version,
		Sealed:   sealed,
		IssuedAt: issued.Unix(),
		Expiry:   issued.Add(time.Hour).Unix(),
	}
}

func channelRows(t *testing.T, ids ...uuid.UUID) map[uuid.UUID]testChannel {
	t.Helper()
	var rows []testChannel
	require.NoError(t, storage.GetDb().Raw(`SELECT id, name, source, all_projects, project_ids::text AS project_ids,
		config_encrypted FROM notification_channels WHERE id IN ?`, ids).Scan(&rows).Error)
	byID := map[uuid.UUID]testChannel{}
	for _, row := range rows {
		byID[row.ID] = row
	}
	return byID
}

// cleanChannels removes the channels and state this test writes. Other
// central channels may exist: apply drops every central channel not sent,
// so they are left alone only when they were never central.
func cleanChannels(t *testing.T, ids ...uuid.UUID) {
	t.Helper()
	require.NoError(t, storage.GetDb().Exec("DELETE FROM shared_config_state").Error)
	t.Cleanup(func() {
		storage.GetDb().Exec("DELETE FROM notification_channels WHERE id IN ?", ids)
		storage.GetDb().Exec("DELETE FROM shared_config_state")
	})
}
