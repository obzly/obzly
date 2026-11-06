// Package sso_services: a connector's client secret is encrypted at rest with
// SecretKeyRepository's shared AES-256-GCM helper.
package sso_services

import (
	user_repositories "obzly/internal/features/users/repositories"
)

func encryptSecret(secretKeyRepository *user_repositories.SecretKeyRepository, plaintext string) (string, error) {
	return secretKeyRepository.Encrypt(plaintext)
}

func decryptSecret(secretKeyRepository *user_repositories.SecretKeyRepository, encoded string) (string, error) {
	return secretKeyRepository.Decrypt(encoded)
}
