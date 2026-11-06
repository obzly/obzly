package users_repositories

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"sync"

	user_models "obzly/internal/features/users/models"
	"obzly/internal/storage"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

type SecretKeyRepository struct{}

var errCiphertextTooShort = errors.New("ciphertext too short")

// secretKeyLockID serializes first-time creation across processes.
const secretKeyLockID = 7_302_114_551

// The secret is created once and never rotated, so every authenticated
// request reading it from Postgres was pure overhead.
var (
	cachedSecretMu sync.RWMutex
	cachedSecret   string
)

func (r *SecretKeyRepository) GetSecretKey() (string, error) {
	cachedSecretMu.RLock()
	secret := cachedSecret
	cachedSecretMu.RUnlock()
	if secret != "" {
		return secret, nil
	}

	secret, err := loadOrCreateSecretKey()
	if err != nil {
		return "", err
	}
	cachedSecretMu.Lock()
	cachedSecret = secret
	cachedSecretMu.Unlock()
	return secret, nil
}

func loadOrCreateSecretKey() (string, error) {
	var secretKey user_models.SecretKey
	err := storage.GetDb().First(&secretKey).Error
	if err == nil {
		return secretKey.Secret, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return "", err
	}

	// Concurrent first callers (parallel test packages, replicas starting
	// together) must not each create a secret: data encrypted with the
	// losing one could never be decrypted.
	err = storage.GetDb().Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec("SELECT pg_advisory_xact_lock(?)", secretKeyLockID).Error; err != nil {
			return err
		}
		err := tx.First(&secretKey).Error
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		secretKey = user_models.SecretKey{Secret: uuid.New().String() + uuid.New().String()}
		return tx.Create(&secretKey).Error
	})
	if err != nil {
		return "", fmt.Errorf("failed to create secret key: %w", err)
	}
	return secretKey.Secret, nil
}

// DeriveKey derives a 32-byte key from the DB secret via SHA-256; it's the AES
// key for Encrypt/Decrypt and the HMAC key for SSO state tokens.
func (r *SecretKeyRepository) DeriveKey() ([]byte, error) {
	secret, err := r.GetSecretKey()
	if err != nil {
		return nil, fmt.Errorf("failed to get secret key: %w", err)
	}
	key := sha256.Sum256([]byte(secret))
	return key[:], nil
}

// Encrypt/Decrypt (AES-256-GCM) are for secrets that must be recovered in
// plaintext, like OIDC client secrets and TOTP seeds, which can't be hashed.
func (r *SecretKeyRepository) Encrypt(plaintext string) (string, error) {
	key, err := r.DeriveKey()
	if err != nil {
		return "", err
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return "", fmt.Errorf("failed to create cipher: %w", err)
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("failed to create GCM: %w", err)
	}

	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", fmt.Errorf("failed to generate nonce: %w", err)
	}

	ciphertext := gcm.Seal(nonce, nonce, []byte(plaintext), nil)
	return base64.StdEncoding.EncodeToString(ciphertext), nil
}

func (r *SecretKeyRepository) Decrypt(encoded string) (string, error) {
	key, err := r.DeriveKey()
	if err != nil {
		return "", err
	}

	ciphertext, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return "", fmt.Errorf("failed to decode ciphertext: %w", err)
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return "", fmt.Errorf("failed to create cipher: %w", err)
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("failed to create GCM: %w", err)
	}

	nonceSize := gcm.NonceSize()
	if len(ciphertext) < nonceSize {
		return "", errCiphertextTooShort
	}

	nonce, sealed := ciphertext[:nonceSize], ciphertext[nonceSize:]
	plaintext, err := gcm.Open(nil, nonce, sealed, nil)
	if err != nil {
		return "", fmt.Errorf("failed to decrypt: %w", err)
	}

	return string(plaintext), nil
}
