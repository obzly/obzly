package otlp

import (
	"compress/gzip"
	"fmt"
	"io"
	"net/http"
)

// MaxBodyBytes caps an OTLP/HTTP body both raw and gunzipped. The API key is
// checked only after decoding, so without it an unauthenticated body or gzip
// bomb could OOM the process. Matches MaxGRPCRecvMsgBytes.
const MaxBodyBytes = 32 << 20

// ReadBody reads an OTLP/HTTP body, gunzipping it on Content-Encoding: gzip
// (net/http's server never does, and most exporters compress), capped at
// MaxBodyBytes both ways.
func ReadBody(req *http.Request) ([]byte, error) {
	limitedBody := http.MaxBytesReader(nil, req.Body, MaxBodyBytes)
	body := io.Reader(limitedBody)

	if req.Header.Get("Content-Encoding") == "gzip" {
		gzipReader, err := gzip.NewReader(limitedBody)
		if err != nil {
			return nil, fmt.Errorf("failed to open gzip reader: %w", err)
		}
		defer func() { _ = gzipReader.Close() }()
		// +1: reading MaxBodyBytes+1 bytes means the body is over the cap, so it is
		// rejected rather than silently truncated.
		body = io.LimitReader(gzipReader, MaxBodyBytes+1)
	}

	data, err := io.ReadAll(body)
	if err != nil {
		return nil, fmt.Errorf("failed to read request body: %w", err)
	}
	if len(data) > MaxBodyBytes {
		return nil, fmt.Errorf("request body exceeds %d byte limit", MaxBodyBytes)
	}
	return data, nil
}
