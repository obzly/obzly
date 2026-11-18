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
	"sync"
	"time"

	users_models "obzly/internal/features/users/models"
	users_services "obzly/internal/features/users/services"
	"obzly/internal/util/logger"
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

// The internal session carries the user's full rights, so tools may only
// call these read endpoints (paths under /api/v1; {name} matches one path
// segment). A new tool adds its endpoints with allowSiteCalls or
// allowCentralCalls, e.g. from an init() in its own file.
var (
	siteCallAllowlist    = &callAllowlist{}
	centralCallAllowlist = &callAllowlist{}
)

func init() {
	allowCentralCalls(http.MethodGet, "/control-plane/sites", "/site/info")

	allowSiteCalls(http.MethodGet,
		"/projects", "/users/me",
		"/logs/query/fields/{project}",
		"/traces/query/get/{project}/{trace}", "/traces/query/services/{project}",
		"/traces/query/operations/{project}",
		"/service-map/{project}",
		"/metrics/query/names/{project}",
		"/projects/dashboards/{project}", "/projects/dashboards/{project}/{dashboard}",
		"/correlation/sessions/{project}", "/correlation/sessions/{project}/{session}",
		"/cloud-cost/summary", "/cloud-cost/anomalies", "/cloud-cost/recommendations", "/cloud-cost/commitments",
		"/projects/{project}/cost-budgets",
		"/posture/summary", "/posture/coverage",
	)
	// Read-only queries that take their parameters as a JSON body.
	allowSiteCalls(http.MethodPost,
		"/logs/query/execute/{project}", "/logs/query/context/{project}", "/logs/query/execute-sql/{project}",
		"/logs/query/patterns/{project}",
		"/traces/query/list/{project}",
		"/metrics/query/label-values/{project}", "/metrics/query/promql/{project}",
		"/cloud-cost/explore", "/posture/findings",
	)
}

// apiError is a REST error the agent can act on.
type apiError struct {
	status  int
	message string
}

func (e *apiError) Error() string {
	return e.message
}

// callAllowlist holds the (method, path pattern) pairs tools may call.
type callAllowlist struct {
	mu       sync.RWMutex
	patterns map[string][][]string
}

// errCallNotAllowed: a tool tried an endpoint outside the allowlist - a bug
// in the tool, never something the agent can fix.
var errCallNotAllowed = errors.New("internal error: this tool may not call that endpoint")

// allowSiteCalls lets tools call these Site endpoints (see siteCallAllowlist).
func allowSiteCalls(method string, patterns ...string) {
	siteCallAllowlist.add(method, patterns...)
}

// allowCentralCalls lets tools call these endpoints of the server itself.
func allowCentralCalls(method string, patterns ...string) {
	centralCallAllowlist.add(method, patterns...)
}

// siteCall calls a Site-scoped endpoint (logs, traces, metrics, projects...).
func (e *apiExecutor) siteCall(
	ctx context.Context, user *users_models.User, site, method, path string,
	query url.Values, body, out any,
) error {
	if err := checkSiteCall(ctx, method, path); err != nil {
		return err
	}
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
	if err := centralCallAllowlist.check(method, path); err != nil {
		return err
	}
	return e.call(ctx, user, method, "/api/v1"+path, query, nil, out)
}

func (a *callAllowlist) add(method string, patterns ...string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.patterns == nil {
		a.patterns = map[string][][]string{}
	}
	for _, pattern := range patterns {
		a.patterns[method] = append(a.patterns[method], strings.Split(strings.Trim(pattern, "/"), "/"))
	}
}

func (a *callAllowlist) check(method, path string) error {
	if a.matches(method, path) {
		return nil
	}
	logger.GetLogger().Error("mcp tool call refused by the endpoint allowlist", "method", method, "path", path)
	return fmt.Errorf("%w (%s %s)", errCallNotAllowed, method, path)
}

// matches tests the decoded path, so an escaped "/" or ".." in an id can't
// reach another route.
func (a *callAllowlist) matches(method, path string) bool {
	decoded, err := url.PathUnescape(path)
	if err != nil {
		return false
	}
	segments := strings.Split(strings.Trim(decoded, "/"), "/")
	a.mu.RLock()
	defer a.mu.RUnlock()
	for _, pattern := range a.patterns[method] {
		if matchSegments(pattern, segments) {
			return true
		}
	}
	return false
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
	if err := e.serve(ctx, recorder, req); err != nil {
		return err
	}

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

// serve runs the handler and stops waiting when ctx ends (tool timeout,
// client gone). Handlers that honour the request context (log queries)
// stop too; the recorder is only read once the handler returned.
func (e *apiExecutor) serve(ctx context.Context, recorder http.ResponseWriter, req *http.Request) error {
	done := make(chan any, 1)
	go func() {
		defer func() { done <- recover() }()
		e.engine.ServeHTTP(recorder, req)
	}()
	select {
	case recovered := <-done:
		if recovered != nil {
			logger.GetLogger().Error("mcp tool call panicked", "path", req.URL.Path, "panic", recovered)
			return errors.New("internal error while running the query")
		}
		return nil
	case <-ctx.Done():
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return fmt.Errorf("the query did not finish within %s and was stopped - narrow the window or add "+
				"filters", toolTimeout)
		}
		return errors.New("the call was cancelled")
	}
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

func matchSegments(pattern, segments []string) bool {
	if len(pattern) != len(segments) {
		return false
	}
	for i, part := range pattern {
		segment := segments[i]
		if segment == "" || segment == "." || segment == ".." {
			return false
		}
		if strings.HasPrefix(part, "{") && strings.HasSuffix(part, "}") {
			continue
		}
		if part != segment {
			return false
		}
	}
	return true
}

func gunzip(payload []byte) ([]byte, error) {
	reader, err := gzip.NewReader(bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	defer func() { _ = reader.Close() }()
	return io.ReadAll(reader)
}
