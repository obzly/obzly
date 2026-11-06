package mcp_server

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"time"

	users_models "obzly/internal/features/users/models"
	users_services "obzly/internal/features/users/services"
)

// apiExecutor runs a tool's REST call in-process against this server's own
// router, as the calling user. On a control plane the path goes through
// /sites/{site}/api/v1, so the existing proxy does the site lookup, grant
// check, path policy and token exchange.
type apiExecutor struct {
	engine       http.Handler
	controlPlane bool
}

var siteIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

const internalSessionTTL = 2 * time.Minute

// apiError is a REST error the agent can act on.
type apiError struct {
	status  int
	message string
}

func (e *apiError) Error() string {
	return e.message
}

// siteCall calls a Site-scoped endpoint (logs, traces, metrics, projects...).
func (e *apiExecutor) siteCall(
	ctx context.Context, user *users_models.User, site, method, path string,
	query url.Values, body, out any,
) error {
	prefix := "/api/v1"
	if e.controlPlane {
		if site == "" {
			return errors.New(`"site" is required on this server - call list_sites to see the ones you can use`)
		}
		if !siteIDPattern.MatchString(site) {
			return fmt.Errorf("invalid site id %q", site)
		}
		prefix = "/sites/" + site + "/api/v1"
	}
	return e.call(ctx, user, method, prefix+path, query, body, out)
}

// centralCall calls an endpoint served by this server itself (identity, site list).
func (e *apiExecutor) centralCall(
	ctx context.Context, user *users_models.User, method, path string, query url.Values, out any,
) error {
	return e.call(ctx, user, method, "/api/v1"+path, query, nil, out)
}

func (e *apiExecutor) call(
	ctx context.Context, user *users_models.User, method, path string,
	query url.Values, body, out any,
) error {
	// Only a token created by a session that passed MFA may stand in for
	// it; otherwise the normal MFA rule applies to the call.
	var extraClaims map[string]any
	if satisfied, _ := ctx.Value(tokenMFAKey{}).(bool); satisfied {
		extraClaims = map[string]any{users_services.AuthOriginClaim: users_services.AuthOriginAccessToken}
	}
	session, err := users_services.GetUserService().
		GenerateAccessTokenWithOptions(user, users_services.AccessTokenOptions{
			TTL:         internalSessionTTL,
			ExtraClaims: extraClaims,
			NotASignIn:  true,
		})
	if err != nil {
		return fmt.Errorf("failed to authorize the call: %w", err)
	}

	// Never nil: handlers (the control-plane proxy) read the body the way
	// a real server request always allows.
	var reader io.Reader = http.NoBody
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("failed to encode request: %w", err)
		}
		reader = bytes.NewReader(encoded)
	}

	target := path
	if len(query) > 0 {
		target += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, method, target, reader)
	if err != nil {
		return fmt.Errorf("failed to build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+session.Token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if remote, ok := ctx.Value(remoteAddrKey{}).(string); ok {
		req.RemoteAddr = remote
	}

	recorder := &closeNotifyingRecorder{httptest.NewRecorder()}
	e.engine.ServeHTTP(recorder, req)

	payload := recorder.Body.Bytes()
	if strings.EqualFold(recorder.Header().Get("Content-Encoding"), "gzip") {
		if payload, err = gunzip(payload); err != nil {
			return fmt.Errorf("failed to read response: %w", err)
		}
	}

	if recorder.Code >= 400 {
		return &apiError{status: recorder.Code, message: errorMessage(recorder.Code, payload)}
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(payload, out); err != nil {
		return fmt.Errorf("failed to decode response: %w", err)
	}
	return nil
}

type remoteAddrKey struct{}

// tokenMFAKey marks a tool call made with a token whose creator passed MFA.
type tokenMFAKey struct{}

// closeNotifyingRecorder: the control-plane reverse proxy asks its writer
// for CloseNotify, which a bare recorder lacks (it would panic).
type closeNotifyingRecorder struct{ *httptest.ResponseRecorder }

func (r *closeNotifyingRecorder) CloseNotify() <-chan bool { return make(chan bool) }

func errorMessage(status int, payload []byte) string {
	var body struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(payload, &body) == nil && body.Error != "" {
		return body.Error
	}
	switch status {
	case http.StatusNotFound:
		return "not found"
	case http.StatusForbidden:
		return "access denied"
	default:
		return fmt.Sprintf("request failed with status %d", status)
	}
}

func gunzip(payload []byte) ([]byte, error) {
	reader, err := gzip.NewReader(bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	defer func() { _ = reader.Close() }()
	return io.ReadAll(reader)
}
