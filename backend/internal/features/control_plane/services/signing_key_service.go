package control_plane_services

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"sync"
	"time"

	control_plane_models "obzly/internal/features/control_plane/models"
	control_plane_repositories "obzly/internal/features/control_plane/repositories"
	users_interfaces "obzly/internal/features/users/interfaces"
	users_models "obzly/internal/features/users/models"
	users_repositories "obzly/internal/features/users/repositories"

	"github.com/go-jose/go-jose/v4"
	"github.com/google/uuid"
)

var ErrSigningKeyForbidden = errors.New("only the root administrator can rotate signing keys")

// jwksRetentionAfterRotation is how long a retired key stays in JWKS(), long enough that
// every Site's cached JWKS has refreshed and pre-rotation assertions still verify.
const jwksRetentionAfterRotation = 24 * time.Hour

// jwksCacheTTL is short: after a rotation on another replica, a Site refetching on the
// new kid may get this replica's old set for this long.
const jwksCacheTTL = 30 * time.Second

type SigningKeyService struct {
	repository          *control_plane_repositories.SigningKeyRepository
	secretKeyRepository *users_repositories.SecretKeyRepository
	auditLogWriter      users_interfaces.AuditLogWriter
	jwksCache           struct {
		sync.Mutex
		value   *jose.JSONWebKeySet
		expires time.Time
	}
}

func (s *SigningKeyService) SetAuditLogWriter(writer users_interfaces.AuditLogWriter) {
	s.auditLogWriter = writer
}

// EnsureActiveKey creates the first signing key if none exists yet.
func (s *SigningKeyService) EnsureActiveKey() error {
	active, err := s.repository.GetActive()
	if err != nil {
		return fmt.Errorf("failed to check active signing key: %w", err)
	}
	if active != nil {
		return nil
	}
	_, err = s.generateAndStore()
	return err
}

func (s *SigningKeyService) generateAndStore() (kid string, err error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return "", fmt.Errorf("failed to generate signing key: %w", err)
	}

	privateDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return "", fmt.Errorf("failed to marshal private key: %w", err)
	}
	privatePEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: privateDER})

	publicDER, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		return "", fmt.Errorf("failed to marshal public key: %w", err)
	}
	publicPEM := pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: publicDER})

	encryptedPrivate, err := s.secretKeyRepository.Encrypt(string(privatePEM))
	if err != nil {
		return "", fmt.Errorf("failed to encrypt private key: %w", err)
	}

	record := &control_plane_models.SigningKey{
		Kid:                 uuid.NewString(),
		Algorithm:           "ES256",
		PrivateKeyEncrypted: encryptedPrivate,
		PublicKeyPEM:        string(publicPEM),
		CreatedAt:           time.Now().UTC(),
	}
	if err := s.repository.Create(record); err != nil {
		return "", err
	}
	s.forgetJWKS()
	return record.Kid, nil
}

// Rotate creates the new key before retiring the others, so a failed generation never
// leaves the control plane without an active key. Root-admin only: the signing key is
// as sensitive as the app-wide secret.
func (s *SigningKeyService) Rotate(user *users_models.User) error {
	if !user.IsRootAdmin {
		return ErrSigningKeyForbidden
	}
	newKid, err := s.generateAndStore()
	if err != nil {
		return err
	}
	if err := s.repository.RetireAllActiveExcept(newKid); err != nil {
		return fmt.Errorf("failed to retire previous signing key: %w", err)
	}
	if s.auditLogWriter != nil {
		s.auditLogWriter.WriteAuditLog("Control-plane signing key rotated", &user.ID, nil)
	}
	return nil
}

// activeSigner returns the active key and its kid.
func (s *SigningKeyService) activeSigner() (kid string, key *ecdsa.PrivateKey, err error) {
	active, err := s.repository.GetActive()
	if err != nil {
		return "", nil, fmt.Errorf("failed to load active signing key: %w", err)
	}
	if active == nil {
		return "", nil, errors.New("no active signing key - has EnsureActiveKey run?")
	}

	plaintext, err := s.secretKeyRepository.Decrypt(active.PrivateKeyEncrypted)
	if err != nil {
		return "", nil, fmt.Errorf("failed to decrypt signing key: %w", err)
	}

	block, _ := pem.Decode([]byte(plaintext))
	if block == nil {
		return "", nil, errors.New("failed to decode signing key PEM")
	}
	parsed, err := x509.ParseECPrivateKey(block.Bytes)
	if err != nil {
		return "", nil, fmt.Errorf("failed to parse signing key: %w", err)
	}

	return active.Kid, parsed, nil
}

// JWKS returns the active key plus keys retired within jwksRetentionAfterRotation.
// Cached for jwksCacheTTL since the endpoint is public.
func (s *SigningKeyService) JWKS() (jose.JSONWebKeySet, error) {
	s.jwksCache.Lock()
	defer s.jwksCache.Unlock()
	if s.jwksCache.value != nil && time.Now().Before(s.jwksCache.expires) {
		return *s.jwksCache.value, nil
	}
	jwks, err := s.loadJWKS()
	if err != nil {
		return jwks, err
	}
	s.jwksCache.value, s.jwksCache.expires = &jwks, time.Now().Add(jwksCacheTTL)
	return jwks, nil
}

func (s *SigningKeyService) forgetJWKS() {
	s.jwksCache.Lock()
	s.jwksCache.value = nil
	s.jwksCache.Unlock()
}

func (s *SigningKeyService) loadJWKS() (jose.JSONWebKeySet, error) {
	keys, err := s.repository.ListPublished(time.Now().UTC().Add(-jwksRetentionAfterRotation))
	if err != nil {
		return jose.JSONWebKeySet{}, fmt.Errorf("failed to list signing keys: %w", err)
	}

	var jwks jose.JSONWebKeySet
	for _, key := range keys {
		block, _ := pem.Decode([]byte(key.PublicKeyPEM))
		if block == nil {
			continue
		}
		publicKey, err := x509.ParsePKIXPublicKey(block.Bytes)
		if err != nil {
			continue
		}

		jwks.Keys = append(jwks.Keys, jose.JSONWebKey{
			Key:       publicKey,
			KeyID:     key.Kid,
			Algorithm: key.Algorithm,
			Use:       "sig",
		})
	}
	// The RS256 key clouds verify federated probe tokens with; Sites accept only
	// ES256.
	if federation, err := federationJWK(); err == nil {
		jwks.Keys = append(jwks.Keys, *federation)
	}
	return jwks, nil
}
