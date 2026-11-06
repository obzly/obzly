// Package security_headers adds baseline security headers to every response, including
// a script-restricting Content-Security-Policy.
//
// The CSP matches what the built SPA loads: its own bundle and runtime-config.js (no
// inline scripts, no eval), antd/ECharts/CodeMirror inline styles, the Jost font from
// Google Fonts, same-origin and data:/blob: images, and same-origin API calls. The
// session token lives in localStorage, so injected markup must not be able to run
// script.
package security_headers

import (
	"strings"

	"github.com/gin-gonic/gin"
)

// ContentSecurityPolicy is sent on every response except the dev-only
// Swagger UI. Update it together with frontend/index.html when the app
// starts loading something from a new origin.
const ContentSecurityPolicy = "default-src 'self'; " +
	"script-src 'self'; " +
	"style-src 'self' 'unsafe-inline' https://fonts.googleapis.com; " +
	"font-src 'self' https://fonts.gstatic.com data:; " +
	"img-src 'self' data: blob:; " +
	"connect-src 'self'; " +
	"object-src 'none'; " +
	"base-uri 'self'; " +
	"frame-ancestors 'none'"

// swaggerPolicy keeps clickjacking protection on the Swagger UI (development
// mode only), whose index.html uses inline scripts.
const swaggerPolicy = "frame-ancestors 'none'"

const swaggerPrefix = "/api/v1/docs/swagger/"

// Middleware sets a fixed set of response headers on every request:
//   - X-Content-Type-Options: nosniff - no MIME sniffing of responses into
//     HTML/JS.
//   - Content-Security-Policy (above), with frame-ancestors 'none', plus
//     X-Frame-Options: DENY for older browsers - no clickjacking.
//   - Referrer-Policy: strict-origin-when-cross-origin - cross-origin
//     requests never see paths or query strings (project IDs, searches).
//   - Strict-Transport-Security - only when the request arrived over HTTPS
//     (directly or per X-Forwarded-Proto). Sending it on a plain-HTTP
//     install would make the app unreachable until TLS is set up.
func Middleware() gin.HandlerFunc {
	return func(ctx *gin.Context) {
		ctx.Header("X-Content-Type-Options", "nosniff")
		ctx.Header("X-Frame-Options", "DENY")
		if strings.HasPrefix(ctx.Request.URL.Path, swaggerPrefix) {
			ctx.Header("Content-Security-Policy", swaggerPolicy)
		} else {
			ctx.Header("Content-Security-Policy", ContentSecurityPolicy)
		}
		ctx.Header("Referrer-Policy", "strict-origin-when-cross-origin")

		// Trusting a client-supplied X-Forwarded-Proto here is safe: it
		// only decides whether HSTS goes back to the same caller.
		if ctx.Request.TLS != nil || ctx.GetHeader("X-Forwarded-Proto") == "https" {
			// 1 year, include subdomains, no preload (an operator decision).
			ctx.Header("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
		}

		ctx.Next()
	}
}
