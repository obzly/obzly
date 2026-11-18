package sharedconfig

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"obzly/internal/storage"
)

// Organisation templates and each channel's emoji switch and template
// override ride the Site's document with the channels; the Site stores them
// as central, and drops templates the control plane no longer sends.
func Test_PayloadAndApply_TemplatesArriveWithTheChannels(t *testing.T) {
	require.NoError(t, storage.GetDb().Exec("DELETE FROM shared_channels").Error)
	require.NoError(t, storage.GetDb().Exec("DELETE FROM notification_templates WHERE source = 'central'").Error)
	t.Cleanup(func() {
		storage.GetDb().Exec("DELETE FROM shared_channels")
		storage.GetDb().Exec("DELETE FROM notification_templates WHERE source = 'central'")
	})
	channelID := uuid.New()
	cleanChannels(t, channelID)
	encrypted, err := secrets.Encrypt(`{"url":"https://hooks.example.com/t"}`)
	require.NoError(t, err)
	now := time.Now().UTC()
	require.NoError(t, storage.GetDb().Create(&SharedChannel{
		ID: channelID, Name: "ops-templates", Kind: "slack", ConfigEncrypted: encrypted, Scope: Scope{AllSites: true},
		CreatedAt: now, UpdatedAt: now, UseEmoji: false, TemplateOverride: true, TemplateBody: "custom {{.Rule.Name}}",
	}).Error)
	require.NoError(t, storage.GetDb().Create(&MessageTemplate{
		Source: SourceCentral, Kind: "slack", Body: "org {{.Rule.Name}}", UpdatedAt: now,
	}).Error)
	siteID := createTestSite(t, "http://127.0.0.1:1", "").ID

	plain, version, err := (&payloadSource{}).payloadFor(siteID)
	require.NoError(t, err)
	var content payload
	require.NoError(t, json.Unmarshal(plain, &content))
	require.Equal(t, []SyncedTemplate{{Kind: "slack", Body: "org {{.Rule.Name}}"}}, content.Templates)
	require.Len(t, content.Channels, 1)
	require.False(t, content.Channels[0].EmojiOn())
	require.True(t, content.Channels[0].TemplateOverride)
	require.Equal(t, "custom {{.Rule.Name}}", content.Channels[0].TemplateBody)

	// The Site side (same database here): the control plane's own rows are
	// replaced by what it receives.
	require.NoError(t, storage.GetDb().Exec("DELETE FROM notification_templates WHERE source = 'central'").Error)
	_, err = apply(documentWith(t, version, now.Add(-time.Minute), content), now)
	require.NoError(t, err)

	var row struct {
		UseEmoji         bool   `gorm:"column:use_emoji"`
		TemplateOverride bool   `gorm:"column:template_override"`
		TemplateBody     string `gorm:"column:template_body"`
	}
	require.NoError(t, storage.GetDb().Raw(`SELECT use_emoji, template_override, template_body
		FROM notification_channels WHERE id = ?`, channelID).Scan(&row).Error)
	require.False(t, row.UseEmoji)
	require.True(t, row.TemplateOverride)
	require.Equal(t, "custom {{.Rule.Name}}", row.TemplateBody)
	templates, err := CentralTemplates()
	require.NoError(t, err)
	require.Len(t, templates, 1)
	require.Equal(t, "org {{.Rule.Name}}", templates[0].Body)

	content.Templates = nil
	_, err = apply(documentWith(t, version+"-2", now, content), now)
	require.NoError(t, err)
	templates, err = CentralTemplates()
	require.NoError(t, err)
	require.Empty(t, templates, "a template the control plane reset is the built-in one again")
}

// A control plane older than message templates sends no useEmoji: emoji stay on.
func Test_SyncedChannel_WithoutUseEmoji_KeepsEmojiOn(t *testing.T) {
	var channel SyncedChannel
	require.NoError(t, json.Unmarshal([]byte(`{"id":"`+uuid.NewString()+`","name":"a","kind":"slack"}`), &channel))
	require.True(t, channel.EmojiOn())
}

func documentWith(t *testing.T, version string, issued time.Time, content payload) *document {
	t.Helper()
	key, err := PublicKey()
	require.NoError(t, err)
	plain, err := json.Marshal(content)
	require.NoError(t, err)
	sealed, err := Seal(key, plain)
	require.NoError(t, err)
	return &document{
		TokenUse: TokenUse, Version: version, Sealed: sealed, IssuedAt: issued.Unix(),
		Expiry: issued.Add(time.Hour).Unix(),
	}
}
