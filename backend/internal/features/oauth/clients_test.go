package oauth

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func Test_ValidateRedirectURI(t *testing.T) {
	for _, ok := range []string{
		"https://claude.ai/api/mcp/auth_callback", "http://localhost:3000/cb", "http://127.0.0.1:8080/cb",
		"cursor://anysphere.cursor-mcp/oauth/callback",
	} {
		assert.NoError(t, validateRedirectURI(ok), ok)
	}
	for _, bad := range []string{
		"http://evil.example/cb", "javascript:alert(1)", "https://a.example/cb#frag", "not a url", "https:///x",
	} {
		assert.Error(t, validateRedirectURI(bad), bad)
	}
}

func Test_RedirectMatches_ExactOrLoopbackAnyPort(t *testing.T) {
	registered := []string{"https://app.example/cb", "http://127.0.0.1/callback"}
	assert.True(t, redirectMatches(registered, "https://app.example/cb"))
	assert.True(t, redirectMatches(registered, "http://127.0.0.1:51234/callback"))
	assert.False(t, redirectMatches(registered, "https://app.example/cb/extra"))
	assert.False(t, redirectMatches(registered, "http://127.0.0.1:51234/other"))
	assert.False(t, redirectMatches(registered, "http://localhost:51234/callback"))
}

func Test_IsMetadataDocumentClientID(t *testing.T) {
	assert.True(t, isMetadataDocumentClientID("https://claude.ai/oauth/mcp-oauth-client-metadata"))
	assert.False(t, isMetadataDocumentClientID("https://claude.ai"))
	assert.False(t, isMetadataDocumentClientID("http://claude.ai/meta"))
	assert.False(t, isMetadataDocumentClientID("obz_dcr_abc"))
}

func Test_IsPublicIP(t *testing.T) {
	for _, private := range []string{"127.0.0.1", "10.1.2.3", "192.168.1.1", "169.254.169.254", "100.64.0.1", "::1", "fd00::1"} {
		assert.False(t, isPublicIP(net.ParseIP(private)), private)
	}
	assert.True(t, isPublicIP(net.ParseIP("8.8.8.8")))
}

func Test_FetchMetadataDocument_ValidatesDocument(t *testing.T) {
	var clientID string
	var doc map[string]any
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(doc)
	}))
	defer server.Close()
	clientID = server.URL + "/client.json"

	restore := useTestClient(server.Client())
	defer restore()

	doc = map[string]any{
		"client_id":     clientID,
		"client_name":   "Claude",
		"redirect_uris": []string{"https://claude.ai/cb"},
	}
	c, err := fetchMetadataDocument(context.Background(), clientID)
	require.NoError(t, err)
	assert.Equal(t, "Claude", c.Name)
	assert.True(t, c.MetadataDocument)

	doc = map[string]any{
		"client_id":     "https://someone-else.example/c",
		"redirect_uris": []string{"https://x.example/cb"},
	}
	_, err = fetchMetadataDocument(context.Background(), clientID)
	assert.ErrorContains(t, err, "does not match")
}

func Test_FetchMetadataDocument_RefusesPrivateAddresses(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	defer server.Close()

	_, err := fetchMetadataDocument(context.Background(), server.URL+"/client.json")
	assert.ErrorContains(t, err, "non-public address")
}

// useTestClient trusts the test server's certificate but keeps the
// public-address dial check off (the test server is on loopback).
func useTestClient(testClient *http.Client) func() {
	original := metadataHTTPClient
	transport := testClient.Transport.(*http.Transport).Clone()
	metadataHTTPClient = &http.Client{Transport: transport, CheckRedirect: original.CheckRedirect}
	return func() { metadataHTTPClient = original }
}
