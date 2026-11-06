# Kubernetes manifests (EKS / Graviton) — control plane

Deploys a control plane (ARCHITECTURE.md "Multi-site: control plane and
Sites") - the central UI/identity layer that fronts one or more
independently-deployed Sites (each still deployed via `../` or `../ha/`, with
that directory's own "Enroll as a Site" section followed). A control plane
has no local data of its own to protect: no PVC, no object storage, no OTLP
listener - it never stores or receives a signal itself, only proxies query
traffic to whichever Site is selected.

Besides sign-in, the Site registry and the proxy, the control plane holds
organizations and teams (which span Sites), serves SCIM 2.0 at `/scim/v2` for
your identity provider, and the MCP endpoint at `/mcp` for AI assistants.
The Helm chart's `mode: control-plane` (`deploy/helm/obzly`) deploys the same
thing.

This is a separate namespace (`obzly-control-plane`) and a separate
Kustomization from `../`/`../ha/` - apply it independently, typically to its
own cluster or at least its own namespace from any Site it fronts (nothing
requires them to share a cluster; a control plane's whole point is fronting
Sites that may be in entirely different clusters/regions/accounts).

## Before applying anything

1. **Postgres**: fill in `DATABASE_DSN` in `secret.yaml` with an externally-
   hosted instance (RDS/Aurora/etc) - this control plane's OWN database,
   never shared with a Site's. Migrations run automatically on startup.
2. **`PUBLIC_URL`** in `configmap.yaml`: this control plane's real, public,
   browser-facing origin (behind whatever Ingress/ALB you point at
   `service.yaml`). Becomes the `iss` of every token-exchange assertion it
   signs - every Site's own `CONTROL_PLANE_ISSUER` must match this exactly,
   character for character - and the origin MCP clients' OAuth uses.
3. **Ingress**: same "bring your own" convention as `../` - `service.yaml` is
   a plain `ClusterIP`. The proxied Live Tail route
   (`/sites/{id}/api/v1/logs/tail/...`) needs `proxy_buffering off` and a
   generous `proxy_read_timeout` (or your ingress controller's equivalent) on
   top of whatever you'd normally configure, the same as any other streaming
   endpoint. If you provision users with SCIM, the identity provider must
   reach `/scim/v2` on the same host (it authenticates with an
   organization's SCIM token, not a session).
4. **Images** are published for `linux/amd64` and `linux/arm64` from v0.47.0;
   `deployment.yaml` pins `kubernetes.io/arch: arm64` for the Graviton
   nodepool these manifests target - remove it on amd64 nodes.

## Applying

```bash
# Everything except secrets:
kubectl apply -k .

# Secrets separately, after filling in secret.yaml:
kubectl apply -f secret.yaml
```

Then, signed in as the bootstrap admin, register each Site from the "Sites"
tab (connectivity check + granting yourself access) - see the main
`README.md`'s multi-site quick-start and `deploy/docker/docker-compose.multisite.yml.example`'s
usage notes for the exact flow, which is identical here.

## Why these design choices

- **`replicas: 2`, `RollingUpdate`** (`deployment.yaml`): unlike `../`'s
  single-replica default, a control-plane outage blocks queries against
  EVERY Site it fronts (ARCHITECTURE.md's trade-offs section) - the one
  piece of this whole feature where under-provisioning replicas is a real
  regression, not just "not yet scaled". It's safe to run N of these: no PVC,
  no per-pod local state, migrations already leader-locked.
- **No PVC, no `OBJECT_STORAGE_URL`, no OTLP port**: a control plane never
  stores or receives a signal itself - see this directory's top-of-file
  comment and ARCHITECTURE.md's non-goals ("persisting/caching proxied data
  centrally").
- **Its own namespace/Postgres/Valkey**, entirely separate from any Site's:
  a control plane's registry/grants/central-identity data must never live in
  the same database as a Site's projects/API keys/local users - see
  ARCHITECTURE.md's identity model section.
