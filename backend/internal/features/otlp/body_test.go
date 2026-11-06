package otlp

import (
	"bytes"
	"compress/gzip"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func Test_ReadBody_PlainBody_ReadAsIs(t *testing.T) {
	req := httptest.NewRequest("POST", "/v1/logs", bytes.NewReader([]byte("hello")))

	body, err := ReadBody(req)
	require.NoError(t, err)
	assert.Equal(t, "hello", string(body))
}

// The Collector's otlphttp exporter gzips by default and net/http's server
// never decompresses; every such export failed with a 400.
func Test_ReadBody_GzipEncoded_Decompressed(t *testing.T) {
	var compressed bytes.Buffer
	gzipWriter := gzip.NewWriter(&compressed)
	_, err := gzipWriter.Write([]byte("hello, compressed"))
	require.NoError(t, err)
	require.NoError(t, gzipWriter.Close())

	req := httptest.NewRequest("POST", "/v1/logs", bytes.NewReader(compressed.Bytes()))
	req.Header.Set("Content-Encoding", "gzip")

	body, err := ReadBody(req)
	require.NoError(t, err)
	assert.Equal(t, "hello, compressed", string(body))
}

func Test_ReadBody_InvalidGzip_ReturnsError(t *testing.T) {
	req := httptest.NewRequest("POST", "/v1/logs", bytes.NewReader([]byte("not actually gzip")))
	req.Header.Set("Content-Encoding", "gzip")

	_, err := ReadBody(req)
	assert.Error(t, err)
}
