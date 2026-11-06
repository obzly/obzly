---
name: site-scoped-api
description: Checklist for adding or moving an Obzly HTTP route in the multi-site design (control plane vs Site, proxy allowlist, frontend base URL, MFA gating). Use whenever a new /api/v1 route or a new frontend API module is added.
---

# Adding a route in the multi-site design

One binary, three roles (`SERVICE_MODE`): standalone/Site (`all`, `api`, `worker`) and `control-plane`. The browser only talks to its own origin; on a control plane, Site data goes through `/sites/{siteId}/api/v1/*`, which proxies to the Site with a per-user Site token.

1. **Pick the side** in `backend/cmd/main.go`:
   - Identity/account routes (users, MFA, SSO, settings, plans, audit) - `setUpIdentityRoutes`, served everywhere.
   - Signal data and projects - `setUpSiteRoutes`, only where `RunsSignalStorage()`.
   - Site registry, grants, signing keys - `setUpControlPlaneRoutes`.
2. **Middleware order**: protected routes sit behind `AuthMiddleware` then `RequireMFAIfEnforced`. MFA self-service routes are registered before the MFA gate so users can enroll.
3. **Proxy policy**: a Site route the UI calls through a control plane must match `proxyAllowlist` in `backend/internal/features/control_plane/services/proxy_policy.go` (denylist wins). Use the route's real path, and add a case to `proxy_policy_test.go`. A missing entry shows up as a 404 only in multi-site mode.
4. **Frontend**: Site routes use `getApiBase()`, identity routes use `getApplicationServer()` (`frontend-react` skill).
5. **Session claims**: `auth_origin` on the token says how the session was authenticated (`control-plane`, `sso-idp-mfa`). Use `users_middleware.SessionSatisfiesMFA(ctx)` rather than re-checking `user.MFAEnabled` when a route cares about MFA.
6. **Verify both modes**: standalone, and through a control plane (the throwaway multi-site compose or the test control plane), since a route can work locally and 404 through the proxy.
