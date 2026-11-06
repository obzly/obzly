package sharedconfig

import (
	"errors"
	"net/http"
	"net/mail"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"obzly/internal/config"
	users_middleware "obzly/internal/features/users/middleware"
	"obzly/internal/util/logger"
	"obzly/internal/util/rate_limit"
)

var errForbidden = errors.New("only admins can change email and notification settings")

// ErrManaged: on a Site enrolled in a control plane, email is managed there.
var ErrManaged = errors.New("this Site's email settings are managed on its control plane")

// documentRequestsPerMinute is per client address; a Site asks every 5
// minutes.
const documentRequestsPerMinute = 30

type Controller struct {
	rateLimiter *rate_limit.RateLimiter
}

type AcceptKeyRequest struct {
	Fingerprint string `json:"fingerprint"`
}

// SMTPView is the stored relay without its password.
type SMTPView struct {
	Configured  bool      `json:"configured"`
	Source      string    `json:"source,omitempty"`
	Host        string    `json:"host"`
	Port        int       `json:"port"`
	Username    string    `json:"username"`
	PasswordSet bool      `json:"passwordSet"`
	From        string    `json:"from"`
	TLS         string    `json:"tls"`
	UpdatedAt   time.Time `json:"updatedAt"`
	// Managed: read-only here, set on the control plane.
	Managed bool `json:"managed"`
}

type TestEmailRequest struct {
	To string `json:"to"`
}

type AdoptRequest struct {
	SiteID string `json:"siteId"`
	Sealed string `json:"sealed"`
}

// SyncView is a control plane's per-Site sync, or a Site's own.
type SyncView struct {
	Mode    string     `json:"mode"`
	Sites   []SiteSync `json:"sites,omitempty"`
	Applied *State     `json:"applied,omitempty"`
	// KeyFingerprint: on a Site, its own key, to compare with what its
	// control plane shows.
	KeyFingerprint string `json:"keyFingerprint,omitempty"`
}

// RegisterPublicRoutes serves Sites (no session): their sealed document and
// the control plane's key.
func (c *Controller) RegisterPublicRoutes(router *gin.Engine) {
	if !config.GetEnv().ServiceMode.IsControlPlane() {
		return
	}
	router.GET("/api/v1/shared-config/:siteId", c.GetDocument)
	router.GET("/api/v1/shared-config-key", c.GetKey)
}

func (c *Controller) RegisterRoutes(router *gin.RouterGroup) {
	router.GET("/settings/smtp", c.GetSMTP)
	router.PUT("/settings/smtp", c.PutSMTP)
	router.DELETE("/settings/smtp", c.DeleteSMTPSettings)
	router.POST("/settings/smtp/test", c.TestSMTP)
	router.GET("/settings/sync", c.GetSync)
	router.POST("/settings/sync/:siteId/accept-key", c.AcceptKey)
	router.POST("/settings/adopt", c.Adopt)
	router.GET("/settings/local-config/export", c.Export)
}

// GetDocument
// @Summary A Site's notification channels and email settings
// @Description For Sites: a document signed with the assertion keys, its payload sealed to the Site's pinned key. The request is signed with that key (X-Obzly-Site-Time, X-Obzly-Applied-Version, X-Obzly-Site-Auth)
// @Tags settings
// @Produce plain
// @Param siteId path string true "Site ID"
// @Param X-Obzly-Site-Time header string true "Unix seconds"
// @Param X-Obzly-Applied-Version header string false "Version the Site applied"
// @Param X-Obzly-Site-Auth header string true "HMAC of the request"
// @Success 200 {string} string
// @Failure 401 {object} map[string]string
// @Failure 404 {object} map[string]string
// @Failure 429 {object} map[string]string
// @Router /shared-config/{siteId} [get]
func (c *Controller) GetDocument(ctx *gin.Context) {
	if !c.allow(ctx) {
		return
	}
	document, err := Document(ctx.Request.Context(), ctx.Param("siteId"), SiteAuth{
		Time:    ctx.GetHeader(HeaderSiteTime),
		Applied: ctx.GetHeader(HeaderAppliedVersion),
		MAC:     ctx.GetHeader(HeaderSiteAuth),
	})
	switch {
	case errors.Is(err, ErrUnknownSite):
		ctx.JSON(http.StatusNotFound, gin.H{"error": "Unknown Site"})
	case errors.Is(err, ErrSiteUnauthenticated):
		ctx.JSON(http.StatusUnauthorized, gin.H{
			"error": "Request not signed with this Site's key: upgrade the Site, or if its key changed, " +
				"accept the new key on the control plane under Obzly settings → Notification channels",
		})
	case errors.Is(err, ErrSiteKeyUnavailable):
		logger.GetLogger().Warn("Failed to read a Site's shared-config key", "site", ctx.Param("siteId"), "error", err)
		ctx.JSON(http.StatusServiceUnavailable, gin.H{"error": "The control plane could not read this Site's key yet"})
	case err != nil:
		logger.GetLogger().Error("Failed to build a Site's shared settings", "site", ctx.Param("siteId"), "error", err)
		ctx.JSON(http.StatusBadGateway, gin.H{"error": "Failed to build the Site's settings"})
	default:
		ctx.Header("Cache-Control", "no-store")
		ctx.Data(http.StatusOK, "application/jwt", []byte(document))
	}
}

// GetKey
// @Summary The control plane's public key
// @Description What a Site seals its own channels to when they move to the control plane
// @Tags settings
// @Produce json
// @Success 200 {object} map[string]string
// @Router /shared-config-key [get]
func (c *Controller) GetKey(ctx *gin.Context) {
	key, err := PublicKey()
	if err != nil {
		ctx.JSON(http.StatusInternalServerError, gin.H{"error": "No key"})
		return
	}
	ctx.JSON(http.StatusOK, gin.H{"publicKey": key})
}

// GetSMTP
// @Summary Email (SMTP) settings
// @Description Admins. The password is never returned
// @Tags settings
// @Produce json
// @Security BearerAuth
// @Success 200 {object} sharedconfig.SMTPView
// @Failure 403 {object} map[string]string
// @Router /settings/smtp [get]
func (c *Controller) GetSMTP(ctx *gin.Context) {
	if !admin(ctx) {
		return
	}
	view := SMTPView{Managed: config.GetEnv().SiteExchangeEnabled()}
	for _, source := range []string{SourceCentral, SourceLocal} {
		stored, err := StoredSMTP(source)
		if err != nil {
			ctx.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to read the email settings"})
			return
		}
		if stored != nil {
			view = SMTPView{
				Configured:  true,
				Source:      source,
				Host:        stored.Host,
				Port:        stored.Port,
				Username:    stored.Username,
				PasswordSet: stored.PasswordEncrypted != "",
				From:        stored.From,
				TLS:         stored.TLS,
				UpdatedAt:   stored.UpdatedAt,
				Managed:     view.Managed,
			}
			break
		}
	}
	ctx.JSON(http.StatusOK, view)
}

// PutSMTP
// @Summary Set the email (SMTP) relay
// @Description Admins. An empty password keeps the stored one. A control plane's relay is sent to its Sites
// @Tags settings
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param request body sharedconfig.SMTP true "Relay"
// @Success 200 {object} map[string]string
// @Failure 400 {object} map[string]string
// @Failure 403 {object} map[string]string
// @Failure 409 {object} map[string]string
// @Router /settings/smtp [put]
func (c *Controller) PutSMTP(ctx *gin.Context) {
	if !admin(ctx) || !writable(ctx) {
		return
	}
	var request SMTP
	if err := ctx.ShouldBindJSON(&request); err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request format"})
		return
	}
	if err := SaveSMTP(ownSource(), request); err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	audit(ctx, "Email (SMTP) settings changed")
	ctx.JSON(http.StatusOK, gin.H{"message": "Email settings saved"})
}

// DeleteSMTPSettings
// @Summary Remove the email (SMTP) relay
// @Tags settings
// @Produce json
// @Security BearerAuth
// @Success 200 {object} map[string]string
// @Failure 403 {object} map[string]string
// @Failure 409 {object} map[string]string
// @Router /settings/smtp [delete]
func (c *Controller) DeleteSMTPSettings(ctx *gin.Context) {
	if !admin(ctx) || !writable(ctx) {
		return
	}
	if err := DeleteSMTP(ownSource()); err != nil {
		ctx.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to remove the email settings"})
		return
	}
	audit(ctx, "Email (SMTP) settings removed")
	ctx.JSON(http.StatusOK, gin.H{"message": "Email settings removed"})
}

// TestSMTP
// @Summary Send a test email
// @Description To the given address, else to the caller's own
// @Tags settings
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param request body sharedconfig.TestEmailRequest false "Recipient"
// @Success 200 {object} map[string]string
// @Failure 400 {object} map[string]string
// @Failure 403 {object} map[string]string
// @Router /settings/smtp/test [post]
func (c *Controller) TestSMTP(ctx *gin.Context) {
	if !admin(ctx) {
		return
	}
	user, _ := users_middleware.GetUserFromContext(ctx)
	var request TestEmailRequest
	_ = ctx.ShouldBindJSON(&request)
	to := request.To
	if to == "" {
		to = user.Email
	}
	if _, err := mail.ParseAddress(to); err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "Give an email address to send the test to"})
		return
	}
	err := SendMail(ctx.Request.Context(), []string{to}, "Test email from Obzly",
		"If you can read this, Obzly can send email through this relay.")
	if err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	ctx.JSON(http.StatusOK, gin.H{"message": "Test email sent to " + to})
}

// GetSync
// @Summary Settings sync
// @Description On a control plane: which version each Site last fetched. On a Site: what it last applied
// @Tags settings
// @Produce json
// @Security BearerAuth
// @Success 200 {object} sharedconfig.SyncView
// @Failure 403 {object} map[string]string
// @Router /settings/sync [get]
func (c *Controller) GetSync(ctx *gin.Context) {
	if !admin(ctx) {
		return
	}
	env := config.GetEnv()
	view := SyncView{Mode: "standalone"}
	var err error
	switch {
	case env.ServiceMode.IsControlPlane():
		view.Mode = "control-plane"
		view.Sites, err = SyncStatus()
	case env.SiteExchangeEnabled():
		view.Mode = "site"
		if view.Applied, err = AppliedState(); err == nil {
			var key string
			key, err = PublicKey()
			view.KeyFingerprint = Fingerprint(key)
		}
	}
	if err != nil {
		ctx.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to read the sync state"})
		return
	}
	ctx.JSON(http.StatusOK, view)
}

// AcceptKey
// @Summary Accept a Site's changed shared-config key
// @Description Control plane, admins. fingerprint is the pending key's, as shown in the sync status
// @Tags settings
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param siteId path string true "Site ID"
// @Param request body sharedconfig.AcceptKeyRequest true "Fingerprint"
// @Success 200 {object} map[string]string
// @Failure 400 {object} map[string]string
// @Failure 403 {object} map[string]string
// @Failure 404 {object} map[string]string
// @Failure 409 {object} map[string]string
// @Router /settings/sync/{siteId}/accept-key [post]
func (c *Controller) AcceptKey(ctx *gin.Context) {
	if !admin(ctx) {
		return
	}
	var request AcceptKeyRequest
	if err := ctx.ShouldBindJSON(&request); err != nil || request.Fingerprint == "" {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "fingerprint is required"})
		return
	}
	siteID := ctx.Param("siteId")
	previous, accepted, err := AcceptKey(siteID, request.Fingerprint)
	switch {
	case errors.Is(err, ErrUnknownSite):
		ctx.JSON(http.StatusNotFound, gin.H{"error": "Unknown Site"})
	case errors.Is(err, ErrNoPendingKey), errors.Is(err, ErrKeyMismatch):
		ctx.JSON(http.StatusConflict, gin.H{"error": err.Error()})
	case err != nil:
		ctx.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to accept the key"})
	default:
		audit(ctx, "Shared-config key of Site "+siteID+" changed from "+previous+" to "+accepted)
		ctx.JSON(http.StatusOK, gin.H{"message": "Key accepted"})
	}
}

// Adopt
// @Summary Move a Site's own channels and email settings to the control plane
// @Description Control plane, admins. sealed is the Site's GET /settings/local-config/export
// @Tags settings
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param request body sharedconfig.AdoptRequest true "Export"
// @Success 200 {object} sharedconfig.AdoptResult
// @Failure 400 {object} map[string]string
// @Failure 403 {object} map[string]string
// @Router /settings/adopt [post]
func (c *Controller) Adopt(ctx *gin.Context) {
	if !admin(ctx) {
		return
	}
	if !config.GetEnv().ServiceMode.IsControlPlane() {
		ctx.JSON(http.StatusNotFound, gin.H{"error": "Only a control plane adopts settings"})
		return
	}
	var request AdoptRequest
	if err := ctx.ShouldBindJSON(&request); err != nil || request.SiteID == "" || request.Sealed == "" {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "siteId and sealed are required"})
		return
	}
	user, _ := users_middleware.GetUserFromContext(ctx)
	result, err := Adopt(request.SiteID, request.Sealed, user.ID)
	if err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	audit(ctx, "Notification channels and email settings moved to the control plane from Site "+request.SiteID)
	ctx.JSON(http.StatusOK, result)
}

// Export
// @Summary This Site's own channels and email settings, sealed to its control plane
// @Description Site, admins: for the control plane's "Move to central"; unreadable to whoever carries it
// @Tags settings
// @Produce json
// @Security BearerAuth
// @Success 200 {object} map[string]string
// @Failure 400 {object} map[string]string
// @Failure 403 {object} map[string]string
// @Router /settings/local-config/export [get]
func (c *Controller) Export(ctx *gin.Context) {
	if !admin(ctx) {
		return
	}
	sealed, err := ExportLocal(ctx.Request.Context())
	if err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	ctx.JSON(http.StatusOK, gin.H{"sealed": sealed})
}

func admin(ctx *gin.Context) bool {
	user, ok := users_middleware.GetUserFromContext(ctx)
	if !ok {
		ctx.JSON(http.StatusUnauthorized, gin.H{"error": "User not authenticated"})
		return false
	}
	if !user.CanUpdateSettings() {
		ctx.JSON(http.StatusForbidden, gin.H{"error": errForbidden.Error()})
		return false
	}
	return true
}

// allow limits each client address on the unauthenticated document route.
func (c *Controller) allow(ctx *gin.Context) bool {
	if c.rateLimiter == nil {
		return true
	}
	key := uuid.NewSHA1(uuid.NameSpaceURL, []byte("shared-config:"+ctx.ClientIP()))
	result, err := c.rateLimiter.CheckRateLimit(key, 1, documentRequestsPerMinute)
	if err == nil && !result.Allowed {
		ctx.Header("Retry-After", strconv.Itoa(result.RetryAfterSec))
		ctx.JSON(http.StatusTooManyRequests, gin.H{"error": "Too many requests"})
		return false
	}
	return true
}

func writable(ctx *gin.Context) bool {
	if config.GetEnv().SiteExchangeEnabled() {
		ctx.JSON(http.StatusConflict, gin.H{"error": ErrManaged.Error()})
		return false
	}
	return true
}

// ownSource is where this deployment stores what its admins set.
func ownSource() string {
	if config.GetEnv().ServiceMode.IsControlPlane() {
		return SourceCentral
	}
	return SourceLocal
}

func audit(ctx *gin.Context, message string) {
	user, _ := users_middleware.GetUserFromContext(ctx)
	if auditLogWriter != nil && user != nil {
		auditLogWriter.WriteAuditLog(message, &user.ID, nil)
	}
}
