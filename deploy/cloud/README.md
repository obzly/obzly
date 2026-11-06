# Cloud connection templates

Read-only access for Obzly's cloud inventory (and, later, cost and posture),
as described in `docs/INFRASTRUCTURE.md` → "Cloud connections". The
connection wizard (Infrastructure → Cloud connections → Add connection)
generates these with every value filled in; use the files here when you
deploy through your own pipeline.

| File | What it creates |
| --- | --- |
| `aws/obzly-readonly-role.yaml` | CloudFormation: a read-only IAM role the probe assumes with an external ID, or through the control plane as an OIDC issuer. Deploy as a StackSet for a whole Organization. |
| `azure/obzly-reader.bicep` | Subscription-scope Bicep: Reader (and optionally Cost Management Reader, Security Reader) for the probe's managed identity, or for a new identity created by `obzly-identity.bicep`. |
| `azure/obzly-identity.bicep` | A user-assigned identity with a federated credential (no secret), trusting your cluster's service account or the control plane. |

Nothing here lets Obzly change a resource or read data in buckets, databases
or secrets. Obzly stores only identifiers (account or subscription ID, role
ARN or client ID, external ID), never keys.
