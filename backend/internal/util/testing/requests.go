package testing

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
)

type RequestOptions struct {
	Method    string
	URL       string
	Body      any
	Headers   map[string]string
	AuthToken string
	// RemoteAddr is the simulated TCP peer (host:port) that ClientIP() reads before any
	// forwarded header. Defaults to randomTestRemoteAddr(); an empty one makes ClientIP()
	// return "".
	RemoteAddr     string
	ExpectedStatus int
}

// randomTestRemoteAddr returns a random 10.0.0.0/8 address. Random per call because
// per-IP lockout counters live in Valkey and persist across tests, so a shared IP would
// let one test's failures lock out another.
func randomTestRemoteAddr() string {
	return RandomTestRemoteAddr()
}

// RandomTestRemoteAddr returns a fresh simulated client address for a test that needs
// the same address across requests (e.g. a per-IP lockout) without colliding with other
// tests or earlier runs. Drawn from 10.0.0.0/8 because a /24 caused birthday-paradox
// collisions between tests.
func RandomTestRemoteAddr() string {
	var b [5]byte
	_, _ = rand.Read(b[:])
	port := 1024 + (int(b[3])<<8+int(b[4]))%(65535-1024)
	return fmt.Sprintf("10.%d.%d.%d:%d", b[0], b[1], b[2], port)
}

type TestResponse struct {
	StatusCode int
	Body       []byte
	Headers    http.Header
}

func MakeGetRequest(
	t *testing.T,
	router *gin.Engine,
	url, authToken string,
	expectedStatus int,
) *TestResponse {
	return makeAuthenticatedRequest(t, router, "GET", url, authToken, nil, expectedStatus)
}

func MakeGetRequestAndUnmarshal(
	t *testing.T,
	router *gin.Engine,
	url, authToken string,
	expectedStatus int,
	responseStruct any,
) *TestResponse {
	return makeAuthenticatedRequestAndUnmarshal(
		t,
		router,
		"GET",
		url,
		authToken,
		nil,
		expectedStatus,
		responseStruct,
	)
}

func MakePostRequest(
	t *testing.T,
	router *gin.Engine,
	url, authToken string,
	body any,
	expectedStatus int,
) *TestResponse {
	return makeAuthenticatedRequest(t, router, "POST", url, authToken, body, expectedStatus)
}

func MakePostRequestAndUnmarshal(
	t *testing.T,
	router *gin.Engine,
	url, authToken string,
	body any,
	expectedStatus int,
	responseStruct any,
) *TestResponse {
	return makeAuthenticatedRequestAndUnmarshal(
		t,
		router,
		"POST",
		url,
		authToken,
		body,
		expectedStatus,
		responseStruct,
	)
}

func MakePutRequest(
	t *testing.T,
	router *gin.Engine,
	url, authToken string,
	body any,
	expectedStatus int,
) *TestResponse {
	return makeAuthenticatedRequest(t, router, "PUT", url, authToken, body, expectedStatus)
}

func MakePutRequestAndUnmarshal(
	t *testing.T,
	router *gin.Engine,
	url, authToken string,
	body any,
	expectedStatus int,
	responseStruct any,
) *TestResponse {
	return makeAuthenticatedRequestAndUnmarshal(
		t,
		router,
		"PUT",
		url,
		authToken,
		body,
		expectedStatus,
		responseStruct,
	)
}

func MakeDeleteRequest(
	t *testing.T,
	router *gin.Engine,
	url, authToken string,
	expectedStatus int,
) *TestResponse {
	return makeAuthenticatedRequest(t, router, "DELETE", url, authToken, nil, expectedStatus)
}

func MakeRequest(t *testing.T, router *gin.Engine, options RequestOptions) *TestResponse {
	var requestBody *bytes.Buffer

	if options.Body != nil {
		bodyJSON, err := json.Marshal(options.Body)
		assert.NoError(t, err, "Failed to marshal request body")
		requestBody = bytes.NewBuffer(bodyJSON)
	} else {
		requestBody = bytes.NewBuffer(nil)
	}

	req, err := http.NewRequest(options.Method, options.URL, requestBody)
	assert.NoError(t, err, "Failed to create HTTP request")

	req.RemoteAddr = options.RemoteAddr
	if req.RemoteAddr == "" {
		req.RemoteAddr = randomTestRemoteAddr()
	}

	if options.Body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	if options.AuthToken != "" {
		req.Header.Set("Authorization", options.AuthToken)
	}

	for key, value := range options.Headers {
		req.Header.Set(key, value)
	}

	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if options.ExpectedStatus != 0 {
		bodyStr := w.Body.String()
		if len(bodyStr) > 100 {
			bodyStr = bodyStr[:100]
			fmt.Println("Body (status code: ", w.Code, "): ", bodyStr)
		} else {
			fmt.Println("Body (status code: ", w.Code, "): ", bodyStr)
		}

		assert.Equal(t, options.ExpectedStatus, w.Code, "Unexpected status code")
	}

	return &TestResponse{
		StatusCode: w.Code,
		Body:       w.Body.Bytes(),
		Headers:    w.Header(),
	}
}

func makeRequestAndUnmarshal(
	t *testing.T,
	router *gin.Engine,
	options RequestOptions,
	responseStruct any,
) *TestResponse {
	response := MakeRequest(t, router, options)

	if responseStruct != nil && len(response.Body) > 0 {
		err := json.Unmarshal(response.Body, responseStruct)
		assert.NoError(t, err, "Failed to unmarshal response body")
	}

	return response
}

func makeAuthenticatedRequest(
	t *testing.T,
	router *gin.Engine,
	method, url, authToken string,
	body any,
	expectedStatus int,
) *TestResponse {
	return MakeRequest(t, router, RequestOptions{
		Method:         method,
		URL:            url,
		Body:           body,
		AuthToken:      authToken,
		ExpectedStatus: expectedStatus,
	})
}

func makeAuthenticatedRequestAndUnmarshal(
	t *testing.T,
	router *gin.Engine,
	method, url, authToken string,
	body any,
	expectedStatus int,
	responseStruct any,
) *TestResponse {
	return makeRequestAndUnmarshal(t, router, RequestOptions{
		Method:         method,
		URL:            url,
		Body:           body,
		AuthToken:      authToken,
		ExpectedStatus: expectedStatus,
	}, responseStruct)
}
