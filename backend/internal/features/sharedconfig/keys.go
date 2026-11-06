// Package sharedconfig is the notification channels and SMTP settings a
// control plane manages for its Sites (docs/configuration.md "Email and
// notification channels"): stored centrally, sent to each Site as a signed
// document sealed to that Site's key, and applied there so alerts still go
// out when the control plane is down. A standalone Site keeps its own.
package sharedconfig

import (
	"bytes"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"sync"
	"time"

	"golang.org/x/crypto/curve25519"
	"golang.org/x/crypto/nacl/box"
	"gorm.io/gorm"

	users_repositories "obzly/internal/features/users/repositories"
	"obzly/internal/storage"
)

// configKey is this deployment's X25519 key pair: a Site's opens the
// documents its control plane seals to it; a control plane's opens the
// channels a Site hands over ("Move to central").
type configKey struct {
	ID                  bool      `gorm:"column:id;primaryKey"`
	PublicKey           string    `gorm:"column:public_key"`
	PrivateKeyEncrypted string    `gorm:"column:private_key_encrypted"`
	CreatedAt           time.Time `gorm:"column:created_at"`
}

func (configKey) TableName() string { return "shared_config_keys" }

var (
	keyMu     sync.Mutex
	keyPublic *[32]byte
	keySecret *[32]byte
	secrets   = &users_repositories.SecretKeyRepository{}
)

// PublicKey is this deployment's public key, base64; the pair is created
// on first use.
func PublicKey() (string, error) {
	public, _, err := keyPair()
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(public[:]), nil
}

// Seal encrypts data so only the holder of publicKey (base64) can read it.
func Seal(publicKey string, data []byte) (string, error) {
	raw, err := base64.StdEncoding.DecodeString(publicKey)
	if err != nil || len(raw) != 32 {
		return "", errors.New("invalid public key")
	}
	var recipient [32]byte
	copy(recipient[:], raw)
	sealed, err := box.SealAnonymous(nil, data, &recipient, rand.Reader)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(sealed), nil
}

// Open decrypts what was sealed to this deployment's key.
func Open(sealed string) ([]byte, error) {
	raw, err := base64.StdEncoding.DecodeString(sealed)
	if err != nil {
		return nil, errors.New("invalid sealed payload")
	}
	public, secret, err := keyPair()
	if err != nil {
		return nil, err
	}
	data, ok := box.OpenAnonymous(nil, raw, public, secret)
	if !ok {
		return nil, errors.New("the payload wasn't sealed to this deployment's key")
	}
	return data, nil
}

// Fingerprint is how an admin compares a public key across the control
// plane and a Site: SHA256: and the base64 of its hash, as SSH shows keys.
func Fingerprint(publicKey string) string {
	raw, err := base64.StdEncoding.DecodeString(publicKey)
	if publicKey == "" || err != nil {
		return ""
	}
	sum := sha256.Sum256(raw)
	return "SHA256:" + base64.RawStdEncoding.EncodeToString(sum[:])
}

// requestKey is what a Site and its control plane both derive from their
// own private key and the other's public key (X25519), so a Site can sign
// its document requests with no secret to distribute.
func requestKey(peerPublic string) ([]byte, error) {
	peer, err := base64.StdEncoding.DecodeString(peerPublic)
	if err != nil || len(peer) != 32 {
		return nil, errors.New("invalid public key")
	}
	public, secret, err := keyPair()
	if err != nil {
		return nil, err
	}
	shared, err := curve25519.X25519(secret[:], peer)
	if err != nil {
		return nil, err
	}
	first, second := public[:], peer
	if bytes.Compare(first, second) > 0 {
		first, second = second, first
	}
	hash := sha256.New()
	hash.Write([]byte("obzly shared-config request v1"))
	hash.Write(shared)
	hash.Write(first)
	hash.Write(second)
	return hash.Sum(nil), nil
}

// requestMAC signs one document request: which Site, when, and the version
// it reports as applied.
func requestMAC(key []byte, siteID, timestamp, applied string) string {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(siteID + "\n" + timestamp + "\n" + applied))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func keyPair() (*[32]byte, *[32]byte, error) {
	keyMu.Lock()
	defer keyMu.Unlock()
	if keyPublic != nil {
		return keyPublic, keySecret, nil
	}
	var stored configKey
	err := storage.GetDb().First(&stored).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		if err := createKey(); err != nil {
			return nil, nil, err
		}
		err = storage.GetDb().First(&stored).Error
	}
	if err != nil {
		return nil, nil, fmt.Errorf("failed to load the shared-config key: %w", err)
	}
	public, err := base64.StdEncoding.DecodeString(stored.PublicKey)
	if err != nil || len(public) != 32 {
		return nil, nil, errors.New("the stored shared-config public key is invalid")
	}
	plain, err := secrets.Decrypt(stored.PrivateKeyEncrypted)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to decrypt the shared-config key: %w", err)
	}
	secret, err := base64.StdEncoding.DecodeString(plain)
	if err != nil || len(secret) != 32 {
		return nil, nil, errors.New("the stored shared-config private key is invalid")
	}
	keyPublic, keySecret = new([32]byte), new([32]byte)
	copy(keyPublic[:], public)
	copy(keySecret[:], secret)
	return keyPublic, keySecret, nil
}

// createKey stores a new pair unless another replica already did.
func createKey() error {
	public, secret, err := box.GenerateKey(rand.Reader)
	if err != nil {
		return err
	}
	encrypted, err := secrets.Encrypt(base64.StdEncoding.EncodeToString(secret[:]))
	if err != nil {
		return err
	}
	return storage.GetDb().Exec(
		`INSERT INTO shared_config_keys (id, public_key, private_key_encrypted, created_at)
		VALUES (TRUE, ?, ?, ?) ON CONFLICT (id) DO NOTHING`,
		base64.StdEncoding.EncodeToString(public[:]), encrypted, time.Now().UTC(),
	).Error
}
