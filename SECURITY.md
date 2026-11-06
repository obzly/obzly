# Security policy

## Reporting a vulnerability

Please report vulnerabilities privately through GitHub: the repository's **Security** tab →
**Report a vulnerability**. Don't open a public issue or pull request for a security problem.

Include what you can of: the affected version (`Settings → Upgrade status`, or the image tag), the
deployment shape (single container, HA, control plane), steps to reproduce, and the impact you
expect. We aim to acknowledge a report within three working days and to agree a disclosure date
with you once a fix is ready.

## Supported versions

Fixes go into the latest release. Upgrade to it to receive them - releases are cut from `main`
and published as `ghcr.io/obzly/obzly:<version>` for `linux/amd64` and `linux/arm64`; the
[upgrade runbook](docs/runbooks/upgrade.md) lists any release that needs action.

## Scope

In scope: the Obzly server (every `SERVICE_MODE`), its web UI, the published image and the
deployment files in [`deploy/`](deploy/). Out of scope: your own Postgres, Valkey, object storage,
identity provider and reverse proxy - though a way Obzly could be made to misuse them is in scope.

## Hardening notes for operators

- Keep the metrics port (`METRICS_ADDR`, `:9464`) and the query executors' result port (`:4319`)
  internal; the Kubernetes manifests and Helm chart do (a NetworkPolicy for the executors). The
  result port is plaintext gRPC; executors run only fragments signed with `QUERIER_TOKEN`, so use
  a long random value, different from `NATS_TOKEN`.
- Give Valkey a password and keep it and the NATS brokers (`:4222`) reachable only from Obzly:
  Valkey holds the API-key cache, token revocations and Live Tail. The Helm chart does both by
  default (`valkey.auth`, `valkey.networkPolicy`, `broker.networkPolicy`).
- The server sends a script-restricting `Content-Security-Policy` (scripts only from its own
  origin). A reverse proxy that injects scripts or rewrites `index.html` has to allow for it.
- Set `TRUSTED_PROXIES` to your load balancer's addresses, never `0.0.0.0/0`. Only those peers'
  `X-Forwarded-For/Host/Proto` are honoured; set `PUBLIC_URL` too, so OAuth/MCP metadata and
  invite links never depend on request headers.
- Restrict alert webhooks with `ALERT_EGRESS_DENYLIST` (e.g. your cluster's service range).
- `/api/v1` request bodies are capped at `API_MAX_BODY_MB` (5 MiB); raise it only if an API
  client needs larger requests.
- Encrypt database backups with a key held outside the database: they contain `secret_keys`, the
  root of trust for sessions, SSO secrets and MFA seeds ([backup runbook](docs/runbooks/backup-restore.md#key-material-secret_keys)).
- Admin accounts must use MFA; an SSO connector set to trust the identity provider's MFA skips
  Obzly's only for sign-ins whose ID token `amr` shows a second factor.
