package control_plane_services

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
	"github.com/google/uuid"

	"obzly/internal/config"
	"obzly/internal/storage"
)

// cloudFederationTokenUse marks tokens a probe exchanges for cloud
// credentials. Sites accept only ES256, so these can't be replayed there.
const cloudFederationTokenUse = "cloud-federation"

type federationKeyRow struct {
	ID                  int       `gorm:"column:id"`
	Kid                 string    `gorm:"column:kid"`
	PrivateKeyEncrypted string    `gorm:"column:private_key_encrypted"`
	PublicKeyPEM        string    `gorm:"column:public_key_pem"`
	CreatedAt           time.Time `gorm:"column:created_at"`
}

func (federationKeyRow) TableName() string { return "cloud_federation_keys" }

var federationKey struct {
	sync.Mutex
	kid string
	key *rsa.PrivateKey
}

// EnsureFederationKey creates the RS256 key on first start; replicas racing
// keep whichever row landed first.
func EnsureFederationKey() error {
	var count int64
	if err := storage.GetDb().Model(&federationKeyRow{}).Count(&count).Error; err != nil {
		return err
	}
	if count > 0 {
		return nil
	}
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return err
	}
	privatePEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	publicDER, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		return err
	}
	encrypted, err := secretKeyRepository.Encrypt(string(privatePEM))
	if err != nil {
		return fmt.Errorf("failed to encrypt the federation key: %w", err)
	}
	defer signingKeyService.forgetJWKS()
	return storage.GetDb().
		Exec(`INSERT INTO cloud_federation_keys (id, kid, private_key_encrypted, public_key_pem, created_at)
		VALUES (1, ?, ?, ?, ?) ON CONFLICT (id) DO NOTHING`,
			uuid.NewString(), encrypted, string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: publicDER})),
			time.Now().UTC()).
		Error
}

// SignCloudToken signs a federation token: issuer this control plane,
// subject probe:<group ID>, the cloud's audience.
func SignCloudToken(subject, audience string, lifetime time.Duration) (string, error) {
	kid, key, err := federationSigner()
	if err != nil {
		return "", err
	}
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: key},
		(&jose.SignerOptions{}).WithType("JWT").WithHeader("kid", kid))
	if err != nil {
		return "", err
	}
	now := time.Now().UTC()
	claims := jwt.Claims{
		Issuer: Issuer(), Subject: subject, Audience: jwt.Audience{audience}, ID: uuid.NewString(),
		IssuedAt: jwt.NewNumericDate(now), NotBefore: jwt.NewNumericDate(now.Add(-5 * time.Second)),
		Expiry: jwt.NewNumericDate(now.Add(lifetime)),
	}
	return jwt.Signed(signer).Claims(claims).Claims(map[string]any{"token_use": cloudFederationTokenUse}).Serialize()
}

// Issuer is the control plane's OIDC issuer URL: PUBLIC_URL without a
// trailing slash, as AWS and Azure compare it exactly.
func Issuer() string {
	return strings.TrimRight(config.GetEnv().PublicURL, "/")
}

// OpenIDConfiguration is the discovery document clouds read to find the JWKS.
func OpenIDConfiguration() map[string]any {
	issuer := Issuer()
	return map[string]any{
		"issuer":                                issuer,
		"jwks_uri":                              issuer + "/.well-known/jwks.json",
		"response_types_supported":              []string{"id_token"},
		"subject_types_supported":               []string{"public"},
		"id_token_signing_alg_values_supported": []string{"RS256"},
		"claims_supported":                      []string{"sub", "aud", "exp", "iat", "iss", "jti"},
	}
}

func federationJWK() (*jose.JSONWebKey, error) {
	var row federationKeyRow
	err := storage.GetDb().Where("id = 1").First(&row).Error
	if err != nil {
		return nil, err
	}
	block, _ := pem.Decode([]byte(row.PublicKeyPEM))
	if block == nil {
		return nil, errors.New("failed to decode the federation key")
	}
	public, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, err
	}
	return &jose.JSONWebKey{Key: public, KeyID: row.Kid, Algorithm: string(jose.RS256), Use: "sig"}, nil
}

func federationSigner() (string, *rsa.PrivateKey, error) {
	federationKey.Lock()
	defer federationKey.Unlock()
	if federationKey.key != nil {
		return federationKey.kid, federationKey.key, nil
	}
	var row federationKeyRow
	if err := storage.GetDb().Where("id = 1").First(&row).Error; err != nil {
		return "", nil, fmt.Errorf("no federation key - has EnsureFederationKey run? %w", err)
	}
	plaintext, err := secretKeyRepository.Decrypt(row.PrivateKeyEncrypted)
	if err != nil {
		return "", nil, fmt.Errorf("failed to decrypt the federation key: %w", err)
	}
	block, _ := pem.Decode([]byte(plaintext))
	if block == nil {
		return "", nil, errors.New("failed to decode the federation key")
	}
	key, err := x509.ParsePKCS1PrivateKey(block.Bytes)
	if err != nil {
		return "", nil, err
	}
	federationKey.kid, federationKey.key = row.Kid, key
	return row.Kid, key, nil
}
