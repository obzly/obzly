package control_plane_controllers

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"path"
	"regexp"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	control_plane_models "obzly/internal/features/control_plane/models"
	control_plane_services "obzly/internal/features/control_plane/services"
	users_middleware "obzly/internal/features/users/middleware"
	"obzly/internal/util/logger"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// proxySiteIDPattern mirrors config.SiteIDPattern; re-checked here because siteId is
// attacker-controlled input.
var proxySiteIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

// proxyMaxRequestBytes bounds a proxied request body. Ingest never goes through the
// proxy, so legitimate bodies here are small.
const proxyMaxRequestBytes = 10 << 20 // 10 MiB

// proxyAccessTTL bounds how stale a cached Site and grant can be: a
// disabled Site or revoked grant takes effect within this long.
const proxyAccessTTL = 3 * time.Second

type proxyAccess struct {
	site    *control_plane_models.Site
	grant   control_plane_services.EffectiveGrant
	expires time.Time
}

var (
	proxyAccessCache  sync.Map // userID|siteID -> proxyAccess
	proxyAccessStores atomic.Uint64
)

// ProxyController fronts every Site of a control-plane deployment under /sites/{id}/...
// (ARCHITECTURE.md "Multi-site: control plane and Sites"). Mounted before the SPA fallback.
type ProxyController struct {
	siteService      *control_plane_services.SiteService
	grantService     *control_plane_services.GrantService
	siteTokenService *control_plane_services.SiteTokenService
}

func (c *ProxyController) RegisterRoutes(r *gin.Engine, authMiddleware, mfaMiddleware gin.HandlerFunc) {
	sites := r.Group("/sites")
	sites.Use(authMiddleware, mfaMiddleware)
	sites.Any("/:siteId/*path", c.Handle)
}

func (c *ProxyController) Handle(ctx *gin.Context) {
	siteID := ctx.Param("siteId")
	if !proxySiteIDPattern.MatchString(siteID) {
		ctx.JSON(http.StatusNotFound, gin.H{"error": "site not found"})
		return
	}

	// path.Clean resolves traversal attempts; the policy check must run after it.
	cleaned := path.Clean(ctx.Param("path"))
	if !control_plane_services.IsProxyPathAllowed(ctx.Request.Method, cleaned) {
		// JSON body, not an empty 404: the SPA shows the body as the error message.
		ctx.JSON(http.StatusNotFound, gin.H{"error": "not found"})
		return
	}

	user, ok := users_middleware.GetUserFromContext(ctx)
	if !ok {
		ctx.Status(http.StatusUnauthorized)
		return
	}

	// Checked on every request (cached briefly) so a revoked grant takes effect in
	// seconds, not when the cached Site token expires.
	access, err := c.resolveAccess(user.ID, siteID)
	if err != nil {
		ctx.JSON(http.StatusInternalServerError, gin.H{"error": "failed to resolve access"})
		return
	}
	site := access.site
	if site == nil || !site.Enabled {
		ctx.JSON(http.StatusNotFound, gin.H{"error": "site not found or disabled"})
		return
	}
	if !access.grant.HasAnyAccess() {
		// A message the SPA can show as-is (apiHelper surfaces the body).
		ctx.JSON(http.StatusForbidden, gin.H{
			"error": "you don't have access to this site - an admin can grant it under Sites -> Grants",
		})
		return
	}

	// Read fully (bounded) rather than streamed so the 401 retry can replay it.
	body, err := io.ReadAll(http.MaxBytesReader(ctx.Writer, ctx.Request.Body, proxyMaxRequestBytes))
	if err != nil {
		ctx.JSON(http.StatusRequestEntityTooLarge, gin.H{"error": "request body too large"})
		return
	}
	rewindBody := func() {
		ctx.Request.Body = io.NopCloser(bytes.NewReader(body))
		ctx.Request.ContentLength = int64(len(body))
		ctx.Request.GetBody = func() (io.ReadCloser, error) {
			return io.NopCloser(bytes.NewReader(body)), nil
		}
	}
	rewindBody()

	// Rewrite to the Site's own path space. RawPath is cleared so the encoded original
	// can't override the cleaned, policy-checked Path.
	ctx.Request.URL.Path = cleaned
	ctx.Request.URL.RawPath = ""

	amr := []string{"pwd"}
	if user.MFAEnabled || users_middleware.SessionSatisfiesMFA(ctx) {
		amr = []string{"mfa"}
	}
	clientIP := ctx.ClientIP()

	token, err := c.siteTokenService.TokenFor(user, site, amr)
	if err != nil {
		// A Site's deliberate refusal is a 403 with its reason; only a failure to
		// reach/complete the exchange is a generic 502.
		var rejected *control_plane_services.SiteRejectedError
		if errors.As(err, &rejected) {
			ctx.JSON(http.StatusForbidden, gin.H{"error": rejected.Message})
			return
		}
		reason := "site unreachable"
		var failed *control_plane_services.SiteExchangeError
		if errors.As(err, &failed) {
			reason = failed.Reason
			if failed.RetryAfter != "" {
				ctx.Header("Retry-After", failed.RetryAfter)
			}
		}
		logger.GetLogger().Warn("site token exchange failed", "site", siteID, "error", err)
		ctx.JSON(http.StatusBadGateway, gin.H{"error": reason})
		return
	}

	proxy, err := control_plane_services.BuildReverseProxy(site, token, clientIP)
	if err != nil {
		ctx.JSON(http.StatusInternalServerError, gin.H{"error": "failed to build proxy"})
		return
	}

	// A 401 means the Site rejected our token (revoked, or rotated secret). Buffer it and
	// retry once with a fresh token: a raw 401 would trigger the frontend's logout of the
	// central session. ownHeaders (security, CORS) are restored before the retry.
	ownHeaders := ctx.Writer.Header().Clone()
	recorder := &bufferingResponseWriter{ResponseWriter: ctx.Writer}
	proxy.ServeHTTP(recorder, ctx.Request)

	if recorder.status != http.StatusUnauthorized {
		return
	}

	c.siteTokenService.Invalidate(user.ID, siteID)
	retryToken, err := c.siteTokenService.TokenFor(user, site, amr)
	if err != nil {
		recorder.flush()
		return
	}

	retryProxy, err := control_plane_services.BuildReverseProxy(site, retryToken, clientIP)
	if err != nil {
		recorder.flush()
		return
	}

	// Reset headers the ReverseProxy copied from the 401, or the retry's stale
	// Content-Length would truncate the real body.
	resetHeaders(ctx.Writer.Header(), ownHeaders)
	rewindBody()

	// The second attempt is final; another 401 becomes a generic 502.
	retryProxy.ServeHTTP(&secondAttemptWriter{ResponseWriter: ctx.Writer, ownHeaders: ownHeaders}, ctx.Request)
}

// resolveAccess returns the Site and the user's effective grant, cached for proxyAccessTTL.
func (c *ProxyController) resolveAccess(userID uuid.UUID, siteID string) (proxyAccess, error) {
	key := userID.String() + "|" + siteID
	now := time.Now()
	if cached, ok := proxyAccessCache.Load(key); ok {
		if access, ok := cached.(proxyAccess); ok && now.Before(access.expires) {
			return access, nil
		}
	}
	site, err := c.siteService.GetSite(siteID)
	if err != nil {
		return proxyAccess{}, err
	}
	access := proxyAccess{site: site, expires: now.Add(proxyAccessTTL)}
	if site != nil && site.Enabled {
		if access.grant, err = c.grantService.ResolveEffectiveGrant(userID, siteID); err != nil {
			return proxyAccess{}, err
		}
	}
	proxyAccessCache.Store(key, access)
	if proxyAccessStores.Add(1)%1024 == 0 {
		proxyAccessCache.Range(func(key, value any) bool {
			if entry, ok := value.(proxyAccess); ok && now.After(entry.expires) {
				proxyAccessCache.Delete(key)
			}
			return true
		})
	}
	return access, nil
}

func resetHeaders(header, to http.Header) {
	for key := range header {
		header.Del(key)
	}
	for key, values := range to {
		header[key] = slices.Clone(values)
	}
}

// bufferingResponseWriter holds back a 401 so Handle can retry; any other status is
// written (and flushed) straight through, so streaming responses are unaffected.
type bufferingResponseWriter struct {
	gin.ResponseWriter
	status    int
	buf       bytes.Buffer
	buffering bool
}

func (w *bufferingResponseWriter) WriteHeader(status int) {
	w.status = status
	if status == http.StatusUnauthorized {
		w.buffering = true
		return
	}
	w.ResponseWriter.WriteHeader(status)
}

func (w *bufferingResponseWriter) Write(b []byte) (int, error) {
	if w.buffering {
		const maxBuffered = 8 << 10 // an auth-error JSON body is tiny; this is a generous cap, not an expectation
		if w.buf.Len() < maxBuffered {
			w.buf.Write(b)
		}
		return len(b), nil
	}
	return w.ResponseWriter.Write(b)
}

// Flush is a no-op while a 401 is buffered: gin's Flush commits a 200 status, which
// would leave the retry unable to set one.
func (w *bufferingResponseWriter) Flush() {
	if w.buffering {
		return
	}
	w.ResponseWriter.Flush()
}

// flush writes out the buffered 401 when no retry was attempted.
func (w *bufferingResponseWriter) flush() {
	if !w.buffering {
		return
	}
	w.buffering = false
	w.ResponseWriter.WriteHeader(w.status)
	_, _ = w.ResponseWriter.Write(w.buf.Bytes())
}

// secondAttemptWriter turns a repeat 401 (fresh token rejected) into a generic 502 and
// drops the Site's own error body.
type secondAttemptWriter struct {
	gin.ResponseWriter
	converting bool
	ownHeaders http.Header
}

func (w *secondAttemptWriter) WriteHeader(status int) {
	if status == http.StatusUnauthorized {
		w.converting = true
		// Drop the headers copied from the Site's 401; the body is ours now.
		resetHeaders(w.Header(), w.ownHeaders)
		w.ResponseWriter.Header().Set("Content-Type", "application/json")
		w.ResponseWriter.WriteHeader(http.StatusBadGateway)
		_, _ = w.ResponseWriter.Write([]byte(`{"error":"site unreachable"}`))
		return
	}
	w.ResponseWriter.WriteHeader(status)
}

func (w *secondAttemptWriter) Write(b []byte) (int, error) {
	if w.converting {
		return len(b), nil // the generic body was already written above
	}
	return w.ResponseWriter.Write(b)
}
