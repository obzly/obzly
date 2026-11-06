package tlsutil_test

import (
	"crypto/tls"
	"crypto/x509"
	"os"
	"path/filepath"
	"testing"
	"time"

	"obzly/internal/util/tlsutil"
	tlsutil_testing "obzly/internal/util/tlsutil/testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func Test_ValidatePair_OnlyOneSet_ReturnsError(t *testing.T) {
	require.Error(t, tlsutil.ValidatePair("CERT", "/c.crt", "KEY", ""))
	require.Error(t, tlsutil.ValidatePair("CERT", "", "KEY", "/c.key"))
	require.NoError(t, tlsutil.ValidatePair("CERT", "", "KEY", ""))
	require.NoError(t, tlsutil.ValidatePair("CERT", "/c.crt", "KEY", "/c.key"))
}

func Test_NewReloadingCertificate_KeyOfAnotherCert_ReturnsError(t *testing.T) {
	dir := t.TempDir()
	certFile, _ := tlsutil_testing.WriteSelfSignedCert(t, dir, "a", "127.0.0.1")
	_, otherKey := tlsutil_testing.WriteSelfSignedCert(t, dir, "b", "127.0.0.1")

	_, err := tlsutil.NewReloadingCertificate(certFile, otherKey)

	require.Error(t, err)
}

// A cert-manager rotation rewrites the mounted files: new handshakes get
// the new certificate without a restart.
func Test_ReloadingCertificate_FilesReplaced_ServesTheNewCertificate(t *testing.T) {
	dir := t.TempDir()
	certFile, keyFile := tlsutil_testing.WriteSelfSignedCert(t, dir, "server", "127.0.0.1")
	reloading, err := tlsutil.NewReloadingCertificate(certFile, keyFile)
	require.NoError(t, err)
	before := leafSerial(t, reloading)

	rotated := t.TempDir()
	newCert, newKey := tlsutil_testing.WriteSelfSignedCert(t, rotated, "server", "127.0.0.1")
	replace(t, newCert, certFile)
	replace(t, newKey, keyFile)

	after := leafSerial(t, reloading)
	assert.NotEqual(t, before, after)
	want, err := tls.LoadX509KeyPair(newCert, newKey)
	require.NoError(t, err)
	assert.Equal(t, want.Certificate[0], mustCert(t, reloading).Certificate[0])
}

func Test_ReloadingCertificate_ReplacedWithMismatchedPair_KeepsThePreviousOne(t *testing.T) {
	dir := t.TempDir()
	certFile, keyFile := tlsutil_testing.WriteSelfSignedCert(t, dir, "server", "127.0.0.1")
	reloading, err := tlsutil.NewReloadingCertificate(certFile, keyFile)
	require.NoError(t, err)
	before := leafSerial(t, reloading)

	newCert, _ := tlsutil_testing.WriteSelfSignedCert(t, t.TempDir(), "server", "127.0.0.1")
	replace(t, newCert, certFile)

	assert.Equal(t, before, leafSerial(t, reloading))
}

func Test_ClientConfig_CAFileWithoutCertificates_ReturnsError(t *testing.T) {
	caFile := filepath.Join(t.TempDir(), "ca.crt")
	require.NoError(t, os.WriteFile(caFile, []byte("not a certificate"), 0o600))

	_, err := tlsutil.ClientConfig(caFile, "")

	require.ErrorContains(t, err, "no PEM certificates")
}

func Test_ClientConfig_NoCAFile_UsesSystemRootsAndTLS12(t *testing.T) {
	config, err := tlsutil.ClientConfig("", "obzly-querier.example.svc")

	require.NoError(t, err)
	assert.Nil(t, config.RootCAs)
	assert.Equal(t, "obzly-querier.example.svc", config.ServerName)
	assert.Equal(t, uint16(tls.VersionTLS12), config.MinVersion)
}

// replace overwrites dst with src's bytes and moves its mtime forward, so
// the change is seen even on a filesystem with coarse timestamps.
func replace(t *testing.T, src, dst string) {
	t.Helper()
	data, err := os.ReadFile(src)
	require.NoError(t, err)
	info, err := os.Stat(dst)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(dst, data, 0o600))
	later := info.ModTime().Add(2 * time.Second)
	require.NoError(t, os.Chtimes(dst, later, later))
}

func mustCert(t *testing.T, reloading *tlsutil.ReloadingCertificate) *tls.Certificate {
	t.Helper()
	cert, err := reloading.GetCertificate(nil)
	require.NoError(t, err)
	return cert
}

func leafSerial(t *testing.T, reloading *tlsutil.ReloadingCertificate) string {
	t.Helper()
	leaf, err := x509.ParseCertificate(mustCert(t, reloading).Certificate[0])
	require.NoError(t, err)
	return leaf.SerialNumber.String()
}
