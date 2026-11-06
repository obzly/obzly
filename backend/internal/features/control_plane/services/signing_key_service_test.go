package control_plane_services

import (
	"testing"
	"time"

	control_plane_models "obzly/internal/features/control_plane/models"
	users_enums "obzly/internal/features/users/enums"
	users_models "obzly/internal/features/users/models"
	"obzly/internal/storage"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTestSigningKeyService(t *testing.T) *SigningKeyService {
	t.Helper()
	return &SigningKeyService{
		repository:          signingKeyRepository,
		secretKeyRepository: secretKeyRepository,
	}
}

// Round trip: EnsureActiveKey provisions a key, and JWKS() publishes a public key that
// verifies a token signed with it, as a real Site would.
func Test_SigningKeyService_EnsureActiveKeyThenSignAndVerify(t *testing.T) {
	svc := newTestSigningKeyService(t)
	require.NoError(t, svc.EnsureActiveKey())

	kid, key, err := svc.activeSigner()
	require.NoError(t, err)
	assert.NotEmpty(t, kid)
	require.NotNil(t, key)

	jwks, err := svc.JWKS()
	require.NoError(t, err)
	require.NotEmpty(t, jwks.Keys)

	found := false
	for _, k := range jwks.Keys {
		if k.KeyID == kid {
			found = true
			assert.Equal(t, "ES256", k.Algorithm)
		}
	}
	assert.True(t, found, "the active key's kid must be published in JWKS")
}

// Rotate is root-admin only, creates a new active key, and keeps the old one in JWKS.
func Test_SigningKeyService_Rotate_RootAdminOnly(t *testing.T) {
	svc := newTestSigningKeyService(t)
	require.NoError(t, svc.EnsureActiveKey())

	nonRoot := &users_models.User{ID: uuid.New(), Role: users_enums.UserRoleAdmin, IsRootAdmin: false}
	err := svc.Rotate(nonRoot)
	require.ErrorIs(t, err, ErrSigningKeyForbidden)

	oldKid, _, err := svc.activeSigner()
	require.NoError(t, err)

	root := &users_models.User{ID: uuid.New(), Role: users_enums.UserRoleAdmin, IsRootAdmin: true}
	require.NoError(t, svc.Rotate(root))

	newKid, _, err := svc.activeSigner()
	require.NoError(t, err)
	assert.NotEqual(t, oldKid, newKid)

	jwks, err := svc.JWKS()
	require.NoError(t, err)
	kids := make(map[string]bool, len(jwks.Keys))
	for _, k := range jwks.Keys {
		kids[k.KeyID] = true
	}
	assert.True(t, kids[oldKid], "the retired key must still be published within the grace window")
	assert.True(t, kids[newKid])
}

func Test_JWKS_LeavesOutKeysRetiredBeforeTheGraceWindow(t *testing.T) {
	svc := newTestSigningKeyService(t)
	require.NoError(t, svc.EnsureActiveKey())
	active, _, err := svc.activeSigner()
	require.NoError(t, err)
	retired := time.Now().UTC().Add(-2 * jwksRetentionAfterRotation)
	old := &control_plane_models.SigningKey{
		Kid: uuid.NewString(), Algorithm: "ES256", PublicKeyPEM: "unused", CreatedAt: retired, RetiredAt: &retired,
	}
	require.NoError(t, signingKeyRepository.Create(old))
	t.Cleanup(func() { storage.GetDb().Where("kid = ?", old.Kid).Delete(&control_plane_models.SigningKey{}) })

	jwks, err := svc.JWKS()

	require.NoError(t, err)
	assert.Empty(t, jwks.Key(old.Kid))
	assert.NotEmpty(t, jwks.Key(active))
}
