package control_plane_services

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"syscall"
	"time"

	"obzly/internal/cache"
	"obzly/internal/config"
	control_plane_models "obzly/internal/features/control_plane/models"
	users_models "obzly/internal/features/users/models"
	users_repositories "obzly/internal/features/users/repositories"

	"github.com/google/uuid"
	"golang.org/x/sync/singleflight"
)

// siteTokenKeyPrefix namespaces the Valkey keys of cached Site tokens per (user, site).
// Values are encrypted with SecretKeyRepository, never stored in plaintext.
const siteTokenKeyPrefix = "cp_site_token:"

// siteTokenExpiryMargin is taken off a token's exp for the cache TTL, so a nearly expired
// token is never handed to the proxy.
const siteTokenExpiryMargin = 5 * time.Minute

// exchangeHTTPClient has a hard timeout, follows no redirects (a bad Site could redirect this
// call anywhere) and uses the pinned, policy-checked dialer from egress.go.
var exchangeHTTPClient = &http.Client{
	Timeout: 10 * time.Second,
	CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
		return errors.New("redirects are not followed for a Site token exchange")
	},
	Transport: newHardenedTransport(),
}

// exchangeResponseMaxBytes caps how much of a Site's response is read, so a bad Site can't
// exhaust memory.
const exchangeResponseMaxBytes = 64 * 1024

type siteTokenExchangeRequest struct {
	Assertion string `json:"assertion"`
}

type siteTokenExchangeResponse struct {
	Token string `json:"token"`
	// Error is the Site's {"error": ...} on a non-200, shown to the user verbatim.
	Error string `json:"error"`
}

// SiteRejectedError is a Site's 4xx refusal of a valid assertion (no grant, not linkable,
// deactivated). The proxy answers 403 with its reason, and it is negative-cached briefly.
type SiteRejectedError struct {
	Status  int
	Message string
}

func (e *SiteRejectedError) Error() string {
	return "site rejected this account: " + e.Message
}

// SiteExchangeError is an exchange that failed other than by refusing the account
// (unreachable, rate-limited, Site error). Reason is safe to show; the wrapped error is for logs.
type SiteExchangeError struct {
	Reason string
	// RetryAfter is the Site's Retry-After on a 429, passed on as is.
	RetryAfter string
	err        error
}

func (e *SiteExchangeError) Error() string { return e.Reason + ": " + e.err.Error() }
func (e *SiteExchangeError) Unwrap() error { return e.err }

// siteTokenDeniedTTL bounds how long a Site's refusal is remembered: long enough that SPA
// retries don't mint an assertion each, short enough that a fixed grant applies quickly.
const (
	siteTokenDeniedKeyPrefix = "cp_site_token_denied:"
	siteTokenDeniedTTL       = 30 * time.Second
)

type SiteTokenService struct {
	assertionService    *AssertionService
	secretKeyRepository *users_repositories.SecretKeyRepository
	group               singleflight.Group
}

// TokenFor returns a valid Site access token for (user, site), exchanging a fresh assertion
// on a cache miss. singleflight collapses concurrent misses into one exchange.
func (s *SiteTokenService) TokenFor(
	user *users_models.User,
	site *control_plane_models.Site,
	amr []string,
) (string, error) {
	// amr is part of the key: a token minted while the session was
	// password-only must not serve a later MFA session, nor the reverse.
	cacheKey := siteTokenCacheKey(user.ID, site.ID, amr)

	if token, ok := s.readCached(cacheKey, site.ApiURL); ok {
		return token, nil
	}
	if rejected := s.readDenied(user.ID, site.ID); rejected != nil {
		return "", rejected
	}

	result, err, _ := s.group.Do(cacheKey, func() (any, error) {
		// Another request may have filled the cache while this one waited.
		if token, ok := s.readCached(cacheKey, site.ApiURL); ok {
			return token, nil
		}
		return s.exchangeAndCache(cacheKey, user, site, amr)
	})
	if err != nil {
		return "", err
	}
	token, _ := result.(string)
	return token, nil
}

// readCached only returns a token minted by the Site at apiURL: after the
// URL changes, tokens the old address issued must never go to the new one.
func (s *SiteTokenService) readCached(cacheKey, apiURL string) (string, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	client := cache.GetCache()
	result := client.Do(ctx, client.B().Get().Key(cacheKey).Build())
	if result.Error() != nil {
		return "", false
	}
	encrypted, err := result.ToString()
	if err != nil {
		return "", false
	}

	plain, err := s.secretKeyRepository.Decrypt(encrypted)
	if err != nil {
		return "", false
	}
	fingerprint, token, found := strings.Cut(plain, "\n")
	if !found || fingerprint != apiURLFingerprint(apiURL) {
		return "", false
	}
	return token, true
}

func (s *SiteTokenService) exchangeAndCache(
	cacheKey string, user *users_models.User, site *control_plane_models.Site, amr []string,
) (string, error) {
	assertion, err := s.assertionService.Mint(user, site.ID, config.GetEnv().PublicURL, amr)
	if err != nil {
		return "", err
	}

	if err := checkEgressAllowed(site.ApiURL); err != nil {
		reason := "site unreachable: its API URL points at an address the control plane may not call"
		var dnsErr *net.DNSError
		if errors.As(err, &dnsErr) {
			reason = unreachableReason(err)
		}
		return "", &SiteExchangeError{Reason: reason, err: err}
	}

	token, err := callSiteExchange(site.ApiURL, assertion)
	if err != nil {
		var rejected *SiteRejectedError
		if errors.As(err, &rejected) {
			s.rememberDenied(user.ID, site.ID, rejected)
		}
		return "", err
	}

	ttl := tokenCacheTTL(token)
	if ttl > 0 {
		encrypted, err := s.secretKeyRepository.Encrypt(apiURLFingerprint(site.ApiURL) + "\n" + token)
		if err == nil {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			client := cache.GetCache()
			_ = client.Do(ctx, client.B().Set().Key(cacheKey).Value(encrypted).Ex(ttl).Build())
		}
	}

	return token, nil
}

// callSiteExchange POSTs assertion to apiURL's exchange endpoint and returns the Site token.
func callSiteExchange(apiURL, assertion string) (string, error) {
	body, err := json.Marshal(siteTokenExchangeRequest{Assertion: assertion})
	if err != nil {
		return "", fmt.Errorf("failed to build exchange request: %w", err)
	}

	req, err := http.NewRequest(
		http.MethodPost,
		strings.TrimRight(apiURL, "/")+"/api/v1/auth/site/exchange",
		strings.NewReader(string(body)),
	)
	if err != nil {
		return "", fmt.Errorf("failed to build exchange request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := exchangeHTTPClient.Do(req)
	if err != nil {
		return "", &SiteExchangeError{Reason: unreachableReason(err), err: err}
	}
	defer func() { _ = resp.Body.Close() }()

	limited := io.LimitReader(resp.Body, exchangeResponseMaxBytes)
	payload, err := io.ReadAll(limited)
	if err != nil {
		return "", fmt.Errorf("failed to read exchange response: %w", err)
	}

	if resp.StatusCode >= 400 && resp.StatusCode < 500 && resp.StatusCode != http.StatusTooManyRequests {
		// The Site's own message about this account: bounded and shown only to that user.
		var parsed siteTokenExchangeResponse
		_ = json.Unmarshal(payload, &parsed)
		message := parsed.Error
		if message == "" {
			message = fmt.Sprintf("the site refused the sign-in (status %d)", resp.StatusCode)
		}
		return "", &SiteRejectedError{Status: resp.StatusCode, Message: message}
	}
	if resp.StatusCode == http.StatusTooManyRequests {
		return "", &SiteExchangeError{
			Reason:     "the site is rate-limiting sign-ins from this control plane, try again shortly",
			RetryAfter: resp.Header.Get("Retry-After"),
			err:        errors.New("site exchange answered 429"),
		}
	}
	if resp.StatusCode != http.StatusOK {
		reason := fmt.Sprintf("the site failed the sign-in (status %d)", resp.StatusCode)
		var parsed siteTokenExchangeResponse
		if json.Unmarshal(payload, &parsed) == nil && parsed.Error != "" {
			reason += ": " + truncate(parsed.Error, 200)
		}
		return "", &SiteExchangeError{
			Reason: reason,
			err:    fmt.Errorf("site exchange failed (status %d)", resp.StatusCode),
		}
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		// Typically a load balancer or ingress answering for the Site.
		return "", &SiteExchangeError{
			Reason: "the site's URL answered with something other than Obzly (check its API URL)",
			err:    fmt.Errorf("site exchange returned content type %q", ct),
		}
	}

	var parsed siteTokenExchangeResponse
	if err := json.Unmarshal(payload, &parsed); err != nil {
		return "", fmt.Errorf("failed to parse exchange response: %w", err)
	}
	if parsed.Token == "" {
		return "", errors.New("site exchange response had no token")
	}
	return parsed.Token, nil
}

// unreachableReason names why the Site's API URL couldn't be reached,
// without echoing addresses beyond what the admin configured.
func unreachableReason(err error) string {
	var dnsErr *net.DNSError
	var opErr *net.OpError
	var netErr net.Error
	switch {
	case errors.As(err, &dnsErr):
		return "site unreachable: its hostname doesn't resolve from the control plane"
	case errors.As(err, &netErr) && netErr.Timeout():
		return "site unreachable: the connection timed out"
	case errors.Is(err, syscall.ECONNREFUSED):
		return "site unreachable: the connection was refused"
	case strings.Contains(err.Error(), "tls:") || strings.Contains(err.Error(), "x509:"):
		return "site unreachable: TLS handshake failed (check the site's certificate)"
	case errors.As(err, &opErr):
		return "site unreachable: the connection failed"
	case strings.Contains(err.Error(), "redirects are not followed"):
		return "site unreachable: its API URL redirects (use the final URL)"
	default:
		return "site unreachable"
	}
}

func truncate(text string, limit int) string {
	if len(text) <= limit {
		return text
	}
	return text[:limit] + "..."
}

// tokenCacheTTL reads the Site token's exp without verifying it (the control plane can't, and
// it only sets our cache lifetime, never authorization) so the entry expires before the token.
func tokenCacheTTL(token string) time.Duration {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return 0
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return 0
	}
	var claims struct {
		Exp int64 `json:"exp"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil || claims.Exp == 0 {
		return 0
	}

	ttl := time.Until(time.Unix(claims.Exp, 0)) - siteTokenExpiryMargin
	if ttl <= 0 {
		return 0
	}
	return ttl
}

// Invalidate drops the cached tokens and any remembered refusal for (userID, siteID), so a
// changed grant or ended session takes effect on the next proxied request.
func (s *SiteTokenService) Invalidate(userID uuid.UUID, siteID string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	client := cache.GetCache()
	_ = client.Do(ctx, client.B().Del().
		Key(siteTokenCacheKeys(userID, siteID)...).
		Key(siteTokenDeniedKeyPrefix+userID.String()+":"+siteID).
		Build())
}

// PurgeSite drops every user's cached token and refusal for siteID, when
// the Site's URL changes or it is removed.
func (s *SiteTokenService) PurgeSite(siteID string) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	client := cache.GetCache()
	patterns := []string{
		siteTokenKeyPrefix + "*:" + siteID, siteTokenKeyPrefix + "*:" + siteID + ":*",
		siteTokenDeniedKeyPrefix + "*:" + siteID, siteTokenDeniedKeyPrefix + "*:" + siteID + ":*",
	}
	for _, node := range client.Nodes() {
		for _, pattern := range patterns {
			var cursor uint64
			for {
				entry, err := node.Do(ctx, node.B().Scan().Cursor(cursor).Match(pattern).Count(500).Build()).
					AsScanEntry()
				if err != nil {
					break
				}
				if len(entry.Elements) > 0 {
					_ = client.Do(ctx, client.B().Del().Key(entry.Elements...).Build())
				}
				if cursor = entry.Cursor; cursor == 0 {
					break
				}
			}
		}
	}
}

// siteTokenAMRVariants are the only amr values the proxy sends.
var siteTokenAMRVariants = [][]string{{"pwd"}, {"mfa"}}

func siteTokenCacheKey(userID uuid.UUID, siteID string, amr []string) string {
	return siteTokenKeyPrefix + userID.String() + ":" + siteID + ":" + strings.Join(amr, ",")
}

// siteTokenCacheKeys is every cached-token key for (user, site), one per amr.
func siteTokenCacheKeys(userID uuid.UUID, siteID string) []string {
	keys := make([]string, 0, len(siteTokenAMRVariants))
	for _, amr := range siteTokenAMRVariants {
		keys = append(keys, siteTokenCacheKey(userID, siteID, amr))
	}
	return keys
}

func (s *SiteTokenService) rememberDenied(userID uuid.UUID, siteID string, rejected *SiteRejectedError) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	client := cache.GetCache()
	_ = client.Do(ctx, client.B().Set().
		Key(siteTokenDeniedKeyPrefix+userID.String()+":"+siteID).
		Value(fmt.Sprintf("%d %s", rejected.Status, rejected.Message)).
		Ex(siteTokenDeniedTTL).
		Build())
}

func (s *SiteTokenService) readDenied(userID uuid.UUID, siteID string) *SiteRejectedError {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	client := cache.GetCache()
	result := client.Do(ctx, client.B().Get().Key(siteTokenDeniedKeyPrefix+userID.String()+":"+siteID).Build())
	if result.Error() != nil {
		return nil
	}
	raw, err := result.ToString()
	if err != nil {
		return nil
	}
	var status int
	var message string
	if n, _ := fmt.Sscanf(raw, "%d ", &status); n != 1 {
		return nil
	}
	if idx := strings.IndexByte(raw, ' '); idx >= 0 {
		message = raw[idx+1:]
	}
	return &SiteRejectedError{Status: status, Message: message}
}

// InvalidateAllForUser is Invalidate for every given Site, used on central logout and
// deactivation.
func (s *SiteTokenService) InvalidateAllForUser(userID uuid.UUID, siteIDs []string) {
	for _, siteID := range siteIDs {
		s.Invalidate(userID, siteID)
	}
}

func apiURLFingerprint(apiURL string) string {
	sum := sha256.Sum256([]byte(strings.TrimRight(apiURL, "/")))
	return hex.EncodeToString(sum[:8])
}
