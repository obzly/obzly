package sso_services

import (
	"testing"

	user_repositories "obzly/internal/features/users/repositories"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func Test_EncryptDecryptSecret_RoundTrips(t *testing.T) {
	repo := &user_repositories.SecretKeyRepository{}

	ciphertext, err := encryptSecret(repo, "super-secret-client-secret")
	require.NoError(t, err)
	assert.NotEqual(t, "super-secret-client-secret", ciphertext, "must not store plaintext")

	plaintext, err := decryptSecret(repo, ciphertext)
	require.NoError(t, err)
	assert.Equal(t, "super-secret-client-secret", plaintext)
}

func Test_EncryptSecret_DifferentCiphertextEachTime(t *testing.T) {
	repo := &user_repositories.SecretKeyRepository{}

	first, err := encryptSecret(repo, "same-plaintext")
	require.NoError(t, err)
	second, err := encryptSecret(repo, "same-plaintext")
	require.NoError(t, err)

	// Random nonce per call: the same plaintext never encrypts the same way twice.
	assert.NotEqual(t, first, second)
}

func Test_DecryptSecret_TamperedCiphertext_Fails(t *testing.T) {
	repo := &user_repositories.SecretKeyRepository{}

	ciphertext, err := encryptSecret(repo, "super-secret-client-secret")
	require.NoError(t, err)

	tampered := ciphertext[:len(ciphertext)-4] + "AAAA"
	_, err = decryptSecret(repo, tampered)
	assert.Error(t, err)
}
