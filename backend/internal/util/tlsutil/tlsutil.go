// Package tlsutil builds the TLS configs for Obzly's own gRPC listeners
// (OTLP gRPC, the query results port) and their clients.
package tlsutil

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	"obzly/internal/util/logger"
)

// ReloadingCertificate serves a cert/key pair from disk and re-reads it when
// either file changes, so a cert-manager rotation (a Secret volume update)
// applies to new connections without a restart.
type ReloadingCertificate struct {
	certFile string
	keyFile  string

	mu        sync.Mutex
	cert      *tls.Certificate
	certStamp fileStamp
	keyStamp  fileStamp
}

type fileStamp struct {
	modTime time.Time
	size    int64
}

// NewReloadingCertificate loads the pair once, failing if it is unreadable
// or the key doesn't match the certificate.
func NewReloadingCertificate(certFile, keyFile string) (*ReloadingCertificate, error) {
	certStamp, err := stampOf(certFile)
	if err != nil {
		return nil, err
	}
	keyStamp, err := stampOf(keyFile)
	if err != nil {
		return nil, err
	}
	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return nil, fmt.Errorf("failed to load TLS certificate %s and key %s: %w", certFile, keyFile, err)
	}
	return &ReloadingCertificate{
		certFile: certFile, keyFile: keyFile,
		cert: &cert, certStamp: certStamp, keyStamp: keyStamp,
	}, nil
}

// GetCertificate is a tls.Config.GetCertificate. Two stats per handshake are
// cheap next to the handshake itself, and gRPC connections are long-lived.
func (r *ReloadingCertificate) GetCertificate(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	certStamp, certErr := stampOf(r.certFile)
	keyStamp, keyErr := stampOf(r.keyFile)
	if certErr != nil || keyErr != nil || (certStamp == r.certStamp && keyStamp == r.keyStamp) {
		return r.cert, nil
	}
	// Recorded even on failure: a half-written pair is retried once either
	// file changes again, not on every handshake.
	r.certStamp, r.keyStamp = certStamp, keyStamp
	cert, err := tls.LoadX509KeyPair(r.certFile, r.keyFile)
	if err != nil {
		logger.GetLogger().Warn("Failed to reload TLS certificate, keeping the previous one",
			"cert", r.certFile, "error", err)
		return r.cert, nil
	}
	r.cert = &cert
	logger.GetLogger().Info("Reloaded TLS certificate", "cert", r.certFile)
	return r.cert, nil
}

// ServerConfig is a TLS 1.2+ server config serving the reloading pair.
func ServerConfig(certFile, keyFile string) (*tls.Config, error) {
	cert, err := NewReloadingCertificate(certFile, keyFile)
	if err != nil {
		return nil, err
	}
	return &tls.Config{MinVersion: tls.VersionTLS12, GetCertificate: cert.GetCertificate}, nil
}

// ClientConfig is a TLS 1.2+ client config trusting caFile, or the system
// roots when caFile is empty. serverName, when set, replaces the dialled
// host in verification (needed when dialling pod IPs that the cert lacks).
func ClientConfig(caFile, serverName string) (*tls.Config, error) {
	config := &tls.Config{MinVersion: tls.VersionTLS12, ServerName: serverName}
	if caFile == "" {
		return config, nil
	}
	pem, err := os.ReadFile(caFile)
	if err != nil {
		return nil, fmt.Errorf("failed to read TLS CA file %s: %w", caFile, err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pem) {
		return nil, fmt.Errorf("TLS CA file %s holds no PEM certificates", caFile)
	}
	config.RootCAs = pool
	return config, nil
}

// ValidatePair: a certificate needs its key and the other way round.
func ValidatePair(certVar, certFile, keyVar, keyFile string) error {
	if (certFile == "") != (keyFile == "") {
		return fmt.Errorf("%s and %s must be set together", certVar, keyVar)
	}
	return nil
}

func stampOf(path string) (fileStamp, error) {
	info, err := os.Stat(path)
	if err != nil {
		return fileStamp{}, fmt.Errorf("failed to read TLS file %s: %w", path, err)
	}
	if info.IsDir() {
		return fileStamp{}, errors.New("TLS file " + path + " is a directory")
	}
	return fileStamp{modTime: info.ModTime(), size: info.Size()}, nil
}
