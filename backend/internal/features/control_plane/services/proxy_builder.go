package control_plane_services

import (
	"bytes"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"time"

	control_plane_models "obzly/internal/features/control_plane/models"
)

// proxyTransport is shared by every proxied request (it pools per host). No overall
// Timeout because Live Tail streams; cancellation comes from the inbound request's
// context. Built on newHardenedTransport, so every dial is egress-checked.
var proxyTransport = func() *http.Transport {
	t := newHardenedTransport()
	t.ResponseHeaderTimeout = siteResponseHeaderTimeout
	return t
}()

// siteResponseHeaderTimeout is longer than a Site's own query deadline (170s), so a slow
// search ends with the Site's 408, not a 502 here. Load balancers need an idle timeout
// at least this long.
const siteResponseHeaderTimeout = 180 * time.Second

// BuildReverseProxy returns a one-shot reverse proxy for one request to site, sending
// token as the Bearer credential. The caller must already have set req.URL.Path to the
// Site-relative path. clientIP (gin's ClientIP, honoring TRUSTED_PROXIES) is forwarded
// as X-Forwarded-For so the Site sees the real user, not the proxy in front of us.
func BuildReverseProxy(site *control_plane_models.Site, token, clientIP string) (*httputil.ReverseProxy, error) {
	target, err := url.Parse(site.ApiURL)
	if err != nil {
		return nil, fmt.Errorf("invalid site api url: %w", err)
	}

	return &httputil.ReverseProxy{
		Rewrite: func(r *httputil.ProxyRequest) {
			// Never forward a browser-supplied credential, cookie or forwarding header;
			// only the Site token this control plane minted is injected.
			for _, header := range []string{
				"Authorization", "Proxy-Authorization", "Cookie", "X-Api-Key",
				"X-Forwarded-For", "X-Forwarded-Host", "X-Forwarded-Proto", "Forwarded",
				"X-Real-Ip", "X-Original-Url", "X-Rewrite-Url",
			} {
				r.Out.Header.Del(header)
			}
			r.SetXForwarded()
			if clientIP != "" {
				r.Out.Header.Set("X-Forwarded-For", clientIP)
			}

			r.Out.URL.Scheme = target.Scheme
			r.Out.URL.Host = target.Host
			r.Out.Host = target.Host
			r.Out.Header.Set("Authorization", "Bearer "+token)
		},
		ModifyResponse: func(resp *http.Response) error {
			// A Site's Set-Cookie would be for the wrong origin's session model.
			resp.Header.Del("Set-Cookie")
			replaceNonJSONServerError(resp, site.ID)
			return nil
		},
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, err error) {
			slog.Default().Warn("site proxy request failed", "site", site.ID, "error", err)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadGateway)
			_, _ = w.Write([]byte(`{"error":"site unreachable"}`))
		},
		// Flush after every write so Live Tail's NDJSON stream isn't buffered.
		FlushInterval: -1,
		Transport:     proxyTransport,
	}, nil
}

// replaceNonJSONServerError replaces a Site's non-JSON 5xx body (e.g. an ingress HTML
// page) with a short JSON error, keeping the status code.
func replaceNonJSONServerError(resp *http.Response, siteID string) {
	if resp.StatusCode < 500 {
		return
	}
	if mediaType, _, err := mime.ParseMediaType(resp.Header.Get("Content-Type")); err == nil &&
		(mediaType == "application/json" || mediaType == "application/problem+json") {
		return
	}

	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
	_ = resp.Body.Close()

	body := fmt.Sprintf(`{"error":%s}`, strconv.Quote(fmt.Sprintf(
		"site %s is unavailable (HTTP %d from the site) - it may be restarting or deploying", siteID, resp.StatusCode,
	)))
	resp.Body = io.NopCloser(bytes.NewReader([]byte(body)))
	resp.ContentLength = int64(len(body))
	resp.Header.Set("Content-Type", "application/json")
	resp.Header.Set("Content-Length", strconv.Itoa(len(body)))
	resp.Header.Del("Content-Encoding")
}
