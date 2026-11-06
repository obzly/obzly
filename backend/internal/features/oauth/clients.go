package oauth

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"obzly/internal/util/egress"
	"strings"
	"time"
)

// client is a resolved OAuth client: registered (RFC 7591) or described by
// a Client ID Metadata Document fetched from its https client_id.
type client struct {
	ID           string   `json:"id"`
	Name         string   `json:"name"`
	URI          string   `json:"uri,omitempty"`
	RedirectURIs []string `json:"redirectUris"`
	// MetadataDocument: identity proven by its https URL, not self-registered.
	MetadataDocument bool `json:"metadataDocument"`
}

const (
	maxMetadataBytes   = 64 << 10
	metadataTimeout    = 5 * time.Second
	maxRedirectURIs    = 10
	maxClientNameChars = 100
)

var errUnknownClient = errors.New("unknown client_id")

// validateRedirectURI accepts https URIs, http on loopback, and private-use
// schemes for native apps; never fragments or script-capable schemes.
func validateRedirectURI(raw string) error {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme == "" || parsed.Fragment != "" {
		return fmt.Errorf("invalid redirect_uri %q", raw)
	}
	switch strings.ToLower(parsed.Scheme) {
	case "https":
		if parsed.Host == "" {
			return fmt.Errorf("invalid redirect_uri %q", raw)
		}
		return nil
	case "http":
		if isLoopbackHost(parsed.Hostname()) {
			return nil
		}
		return fmt.Errorf("redirect_uri %q must use https (http only on localhost)", raw)
	case "javascript", "data", "file", "vbscript", "about", "blob":
		return fmt.Errorf("redirect_uri scheme %q is not allowed", parsed.Scheme)
	default:
		return nil
	}
}

// redirectMatches is exact matching, except that loopback redirects may use
// any port (RFC 8252 section 7.3).
func redirectMatches(registered []string, requested string) bool {
	for _, candidate := range registered {
		if candidate == requested {
			return true
		}
		a, errA := url.Parse(candidate)
		b, errB := url.Parse(requested)
		if errA != nil || errB != nil {
			continue
		}
		if a.Scheme == "http" && b.Scheme == "http" && isLoopbackHost(a.Hostname()) &&
			a.Hostname() == b.Hostname() && a.Path == b.Path && a.RawQuery == b.RawQuery {
			return true
		}
	}
	return false
}

func isLoopbackHost(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// isMetadataDocumentClientID: https URL with a path, per the CIMD draft.
func isMetadataDocumentClientID(clientID string) bool {
	parsed, err := url.Parse(clientID)
	return err == nil && parsed.Scheme == "https" && parsed.Host != "" &&
		parsed.Path != "" && parsed.Path != "/" && parsed.Fragment == "" && parsed.User == nil
}

type metadataDocument struct {
	ClientID     string   `json:"client_id"`
	ClientName   string   `json:"client_name"`
	ClientURI    string   `json:"client_uri"`
	RedirectURIs []string `json:"redirect_uris"`
}

func fetchMetadataDocument(ctx context.Context, clientID string) (*client, error) {
	ctx, cancel := context.WithTimeout(ctx, metadataTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, clientID, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := metadataHTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch client metadata: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("client metadata returned status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxMetadataBytes+1))
	if err != nil || len(body) > maxMetadataBytes {
		return nil, errors.New("client metadata is too large or unreadable")
	}
	var doc metadataDocument
	if err := json.Unmarshal(body, &doc); err != nil {
		return nil, errors.New("client metadata is not valid JSON")
	}
	if doc.ClientID != clientID {
		return nil, errors.New("client metadata client_id does not match its URL")
	}
	if err := validateRedirectURIs(doc.RedirectURIs); err != nil {
		return nil, err
	}

	parsed, _ := url.Parse(clientID)
	name := strings.TrimSpace(doc.ClientName)
	if name == "" {
		name = parsed.Hostname()
	}
	return &client{
		ID: clientID, Name: truncate(name, maxClientNameChars), URI: doc.ClientURI,
		RedirectURIs: doc.RedirectURIs, MetadataDocument: true,
	}, nil
}

func validateRedirectURIs(uris []string) error {
	if len(uris) == 0 || len(uris) > maxRedirectURIs {
		return fmt.Errorf("between 1 and %d redirect_uris are required", maxRedirectURIs)
	}
	for _, uri := range uris {
		if err := validateRedirectURI(uri); err != nil {
			return err
		}
	}
	return nil
}

// metadataHTTPClient fetches public client documents only: no proxies, no
// redirects, TLS 1.2+, and every dialed address must be public.
var metadataHTTPClient = &http.Client{
	Timeout: metadataTimeout,
	CheckRedirect: func(*http.Request, []*http.Request) error {
		return errors.New("redirects are not followed")
	},
	Transport: &http.Transport{
		Proxy:               nil,
		DialContext:         publicOnlyDialContext,
		TLSClientConfig:     &tls.Config{MinVersion: tls.VersionTLS12},
		TLSHandshakeTimeout: metadataTimeout,
	},
}

var publicIPPolicy = isPublicIP

func publicOnlyDialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	policy := func(ip net.IP) error {
		if !publicIPPolicy(ip) {
			return fmt.Errorf("refusing to fetch client metadata from non-public address %s", ip)
		}
		return nil
	}
	return egress.DialContext(policy, &net.Dialer{Timeout: metadataTimeout})(ctx, network, addr)
}

func isPublicIP(ip net.IP) bool {
	return egress.IsPublic(ip)
}

func truncate(value string, limit int) string {
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit])
}
