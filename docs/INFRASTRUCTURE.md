# Infrastructure inventory (design)

Status: approved 2026-10-25 with its UI mock. The **basic inventory** ships in v0.55.0 with the
pending fixes, Synthetics and shared notification channels. **Cloud connections** and the cloud
inventory follow in v0.56.0; cost and posture uses reuse the same connections later - see
[`CLOUD_COST_POSTURE.md`](CLOUD_COST_POSTURE.md).

Icons: the UI uses the official Kubernetes community icons (CC BY 4.0), AWS Architecture Icons and
Azure public service icons, credited in `NOTICE`.

## Goal

One place that lists every asset Obzly knows about, across all Sites, with its health, performance
and lifecycle:

- hosts and VMs, Kubernetes clusters, nodes, namespaces, workloads, pods and containers, Docker
  containers, and services;
- later, cloud resources that run no agent: load balancers, databases, buckets, functions.

The basic inventory needs nothing new from the user. It is built from what the OTel agents already
send. The cloud inventory needs read-only cloud access, given to the probe agent and never stored in
Obzly.

## What the agents already send

- Every metric sample carries **all of its OTLP resource attributes as labels**, normalized:
  `k8s.pod.name` → `k8s_pod_name`, `host.id` → `host_id`, `cloud.region` → `cloud_region`
  (`util/attrnames`). Logs keep them in `fields` and promote cluster, namespace, pod, container,
  node, service and cloud into columns. Spans keep them in `resource_attributes`.
- **Lifecycle is already recorded:**
  - the `metrics_series` stream has one row per series with `first_seen` / `last_seen`;
  - `metric_label_index` in Postgres has each label value with first / last seen;
  - the `logs_1m` rollup counts lines per minute by pod, container, namespace and service.
- Existing derivations to reuse: pod → ReplicaSet → workload from the pod-name hash
  (`logs/querier/deploys.go`, used by K8s Rollouts); service → namespace / deployment / node
  (`spans/querier/k8s_metadata.go`); the node key `host_name` and the PromQL of the K8s Cluster
  Overview dashboard.
- **Gaps** in the current Kubernetes manifest: no `k8s_cluster` receiver (so no pod phase, node
  Ready condition, restart count or replica counts), `k8s_attributes` only on logs (no deployment
  name on metrics), and `k8s.cluster.name` only when the user typed one. See "Agent changes".

## Assets

| Kind | Key | Comes from | Parent |
| --- | --- | --- | --- |
| Cluster | `k8s_cluster_name` | metrics, logs | — |
| Node | cluster + `host_name` | kubelet / host metrics | cluster |
| Host / VM | `host_id`, else `host_name` | host metrics, journald | — (shown as "EC2 instance" / "Azure VM" from `cloud_platform`) |
| Namespace | cluster + namespace | metrics, logs | cluster |
| Workload | cluster + namespace + kind + name | `k8s_*_name` labels, pod-name hash | namespace |
| Pod | cluster + namespace + `k8s_pod_uid` (else name) | kubelet metrics, logs | workload, node |
| Container | pod + container name | kubelet metrics, logs | pod |
| Docker container | host + container id | `docker_stats` | host |
| Service | namespace + `service_name` | traces | workload when known |

Each asset keeps its resource attributes (OS, instance type, region, zone, k8s version, image...)
and which signals it was seen in (metrics, logs, traces, later cloud).

### Lifecycle

- First seen, last seen, age, and restarts / replacements in the last 24 h.
- States:
  - **New**: first seen in the last 24 h;
  - **Active**;
  - **Not reporting**: no data for 10 minutes;
  - **Terminated**: a pod or container gone for 30 minutes (usually replaced), a host or node gone
    for 24 hours.
- Replaced and deleted machines are removed, not kept:
  - a Kubernetes node missing for an hour while other nodes of its cluster still report (a spot
    node reclaimed, a node scaled down);
  - a cloud resource the cloud reports `terminated`, or one missing for an hour (and three
    refreshes) while scans of its account and region still arrive.
  - If the collector or the cloud scans stop instead, nothing is removed: the assets show as not
    reporting.
- Retention: terminated pods and containers are kept 7 days, everything else 30 days
  (`INVENTORY_RETENTION_DAYS`). A project keeps at most 50,000 assets
  (`INVENTORY_MAX_ASSETS_PER_PROJECT`); past that the least recently seen are dropped and the
  worker logs a warning.
- **Job and CronJob runs** come and go every few minutes, so they don't pile up:
  - each run's Job belongs to its CronJob, which is the one workload shown;
  - a run's pod and containers show while it runs and leave once it succeeds;
  - a failed run stays (red, rolled up to its CronJob as amber) until a day after it was last seen;
  - a finished Job that no CronJob owns stays a day.
  - Runs are left out of the change feed's "new" and state entries; a failure still shows as a
    health change.
- A change feed records new assets, terminations, restarts and health changes (kept 7 days).

### Health

Same colours as Synthetics, each with the reason shown in the UI:

| Colour | Health | Examples |
| --- | --- | --- |
| Green | Healthy | all rules pass |
| Amber | Warning | CPU > 90 % for 15 min; memory > 90 %; filesystem > 85 %; ≥ 3 restarts in 1 h; error-log share > 5 % in 15 min; fewer replicas available than desired |
| Red | Critical | host or node not reporting; memory > 97 %; filesystem > 95 %; pod CrashLoopBackOff or Failed; no replicas available |
| Blue | Inactive | terminated or retired |
| Grey | Unknown | no metrics (for example a service seen only in traces) |

- A workload is at least as bad as its worst pod (capped at amber unless its replicas say red);
  a namespace and a cluster roll up their children.
- A firing alert whose labels match an asset (`k8s_pod_name`, `host_name`...) gives it the alert's
  severity and links to it.
- Thresholds are defaults in Phase 1; per-project overrides come later.

## How it's built (Site)

A worker job on each Site, per project every 5 minutes, sharded and locked like the alert
evaluator. Each run:

1. reads resource label combinations with first / last seen from `metrics_series` for a few anchor
   metrics (node and pod memory, container, host memory, pod phase...);
2. reads pod, container and service activity and error counts from `logs_1m`, and services from
   the traces;
3. runs one batch of instant PromQL queries for CPU, memory, disk, restarts, phase and replicas
   (both the newer `*_usage` and older `*_utilization` metric names); a pod's restarts are its
   containers' summed, and the 24 h restart counts are refreshed at most hourly;
4. matches active alerts by labels;
5. writes the assets whose stored fields changed, moves `last_seen` of the rest in one statement,
   and appends changes to `inventory_changes`.

The cost is a handful of small queries per project per 5 minutes, and only the discovery reads
(steps 1 and 2's services) for a project with nothing reporting; nothing reads raw samples or raw
logs.

### API (Site, through the CP proxy)

- `GET /api/v1/inventory/summary`: counts by kind, health and state.
- `GET /api/v1/inventory/assets`: filters (kind, health, state, project, cluster, namespace,
  search), sort, cursor paging. Only projects the user can read.
- `GET /api/v1/inventory/assets/:id`: attributes, parent chain, children, health reasons, recent
  changes.
- `GET /api/v1/inventory/changes`: the change feed.

## Across Sites (control plane)

The browser fans out: for each Site the user can reach (`/control-plane/sites/status`, already
filtered by grants) it calls `/sites/{id}/api/v1/inventory/...` in parallel with the user's own
Site token, and merges the results. A Site that fails shows a chip ("contoso: unavailable")
instead of failing the page. No new control-plane service identity is needed. A standalone Site
shows its own inventory in the same tab.

## UI ("Infrastructure" tab)

- **Overview:**
  - counts by kind with the health split, and a health donut;
  - **needs attention**: red and amber assets with their reasons;
  - **recent changes**: new, terminated, restarts, health changes;
  - top CPU and memory consumers;
  - breakdown by Site, cloud and region, and cluster.
- **Assets:**
  - kind chips and health chips (like the Synthetics status chips), search, and Site / project /
    cluster / namespace filters;
  - **sort from the table headers** with caret icons;
  - a flat ↔ tree toggle (cluster → node → pod, cluster → namespace → workload → pod, host →
    containers);
  - columns: health bullet, kind icon, name, Site / project, cluster / namespace, node / host,
    CPU %, memory %, restarts 24 h, age, last seen, lifecycle tag.
- **Asset detail fly-out** (same pattern as the Synthetics monitor detail):
  - health and reasons; properties (all resource attributes);
  - relationships: parent breadcrumb and children;
  - CPU, memory, network and disk charts (PromQL through the proxy);
  - a lifecycle timeline: first seen, restarts, replacements, last seen;
  - related active alerts and Synthetics monitors that target it;
  - **Open logs** (Logs tab pre-filtered), **Open traces**, **Open dashboard** (K8s Cluster
    Overview or the host template with variables set).

## Agent changes (Kubernetes manifest)

- Add `k8s_attributes` to the **metrics** pipeline, so kubelet metrics carry deployment /
  statefulset / daemonset names. This changes series identity once (new series after the
  upgrade); dashboards are unaffected.
- An optional (on by default) **cluster agent**: a one-replica Deployment with the `k8s_cluster`
  receiver and read-only RBAC, for pod phase, node conditions, container restarts, replica counts
  and Kubernetes versions.
- Fill `k8s.cluster.name` from the EKS detector when the user leaves it empty.
- `file_storage` for file offsets (held fix 6) lands in the same change.
- Clusters on the old manifest still appear, without phase, replicas or restarts, and show
  "re-apply the latest manifest for pod phase, restarts and replicas".

## Cloud connections and cloud inventory (v0.56.0)

### Cloud connections are shared

A **cloud connection** is read-only access to one AWS account (or AWS Organization) or one Azure
subscription (or management group). It is defined once, in **Settings → Cloud connections**
(admins, on the control plane), and every feature that reads from the cloud uses it:

| Use ("scan category") | Feature | Ships |
| --- | --- | --- |
| Infrastructure inventory | resources that run no agent, merged into Assets | v0.56.0 |
| Cost and billing | spend by service, account and tag; forecast; anomalies | later |
| Security posture | misconfiguration findings against CIS / AWS FSBP / Azure benchmarks | later |

A connection records:
- the provider, account or subscription and its scope;
- the access method;
- the probe or probes that use it;
- its enabled uses, each with its own settings (regions and resource types for inventory, the
  export bucket for billing) and its own "last check" status.

Adding a use later doesn't create a new connection: the wizard produces an **updated** template or
script that adds only the new permissions (a CloudFormation stack update, a Bicep redeploy, or the
extra CLI lines), then **Test access** checks each use separately. A use whose permissions are
missing shows "needs permission" with the exact missing actions, and the other uses keep working.

The Infrastructure tab's "Cloud accounts" view lists the connections that have inventory enabled.

### The connection wizard

One step at a time, with Back and Next. The provider comes first, and every later step shows only
that provider's options, names and templates.

1. **Provider:** AWS or Azure. Next is disabled until one is chosen; changing it later resets the
   provider-specific answers.
2. **Scan types:** tick one or more of inventory, cost and billing, security posture. A summary
   lists the read-only permissions they need, for this provider only.
3. **Scope:**
   - AWS: one account (account ID), or the whole Organization (deployed as a StackSet from the
     management account; cost and billing reads from the management account);
   - Azure: one subscription (subscription ID), or a management group.

   Plus the connection's name and the **probe group** that runs the scans.
4. **Access method**, in the provider's own terms:
   - **the probe's own identity:**
     - AWS: the IAM identity (EC2 instance profile, IRSA, EKS Pod Identity);
     - Azure: a managed identity;
   - **an identity in the target:**
     - AWS: a read-only role assumed with an external ID that Obzly generates;
     - Azure: a user-assigned identity with a federated credential for the probe's service account;
   - **federated through the Obzly control plane:** for probes with no cloud identity. The control
     plane is the OIDC issuer; its URL must be reachable from the cloud.
5. **Run template**, generated with every value filled in:
   - **format:** AWS CloudFormation or AWS CLI; Azure ARM / Bicep or az CLI;
   - **actions:** copy or download;
   - **the identifier it outputs:** AWS role ARN, or Azure client ID; not asked for when the probe
     uses its own identity;
   - **"I've run it":** must be ticked before Next is enabled.
6. **Settings and test:**
   - inventory regions (AWS) or locations (Azure), resource types, and how often to run a full
     discovery (default 4 h) and a health scan (default 15 min);
   - **Test access** runs one dry scan per use on a member of the probe group and shows the
     result of each (the first account and up to two regions, so it answers in time);
   - then **Save connection**.

### Permissions per use

| Use | AWS | Azure |
| --- | --- | --- |
| Inventory | `ViewOnlyAccess`, plus `elasticloadbalancing:DescribeTargetHealth`, `tag:GetResources`, `eks:DescribeCluster`, `ec2:DescribeInstanceStatus` | `Reader` |
| Cost and billing, security posture | See [Permissions per use](CLOUD_COST_POSTURE.md#permissions-per-use) in `CLOUD_COST_POSTURE.md` | Same |

Every permission is read-only. Nothing in a template lets Obzly change a resource or read data in
buckets, databases or secrets, apart from the billing export bucket when one is chosen.

### Credentials are never stored in Obzly

- **AWS:**
  - by default, the probe's own identity (EC2 instance profile, IRSA, EKS Pod Identity);
  - for other accounts, `sts:AssumeRole` with an external ID into the read-only role from the
    template;
  - for probes outside AWS, `AssumeRoleWithWebIdentity` with a token issued by the control plane.
- **Azure:** a managed identity, or a user-assigned identity whose federated credential trusts the
  probe's Kubernetes service account or the control plane's issuer.
- **The control plane as OIDC issuer:** it already signs JWTs and serves a JWKS. It issues
  short-lived, audience-bound tokens to the probe (subject `probe:<group ID>`: the ID, not the
  name, so a group deleted and added again under the same name isn't trusted), and AWS or Azure
  exchanges them for cloud credentials.
- **What Obzly stores:** only identifiers (account or subscription ID, role ARN or client ID,
  external ID). No keys or secrets.

### Inventory discovery

- **AWS, per region:** EC2, ELBv2 with target health, EKS, RDS, Auto Scaling, Lambda and S3, plus
  `tag:GetResources` for everything else.
- **Azure:** one Resource Graph query plus Resource Health.
- **Follow-up:** CloudWatch / Azure Monitor health metrics for load balancers and databases.
- **Delivery:** OTLP log records shaped as OTel entity events (`entity.type`, `entity.id`,
  attributes), pushed to a Site project like Synthetics results.
- **Merging:** the builder merges them as `source=cloud` assets and links them to the assets the
  agents report:
  - EC2 instance ID ↔ `host_id`;
  - EKS cluster ↔ `k8s_cluster_name`;
  - ALB ↔ its target instances.
- **Template location:** `deploy/cloud/aws/` and `deploy/cloud/azure/`. The wizard fills them in.

## Implementation notes (v0.56.0, cloud connections)

Where the build differs from, or adds to, the cloud design above:

- **Where:** admins manage connections in **Settings → Cloud connections** on the control plane
  (list and wizard). Infrastructure → Cloud connections shows the same list read-only, with a link
  to Settings for admins. API: `/api/v1/cloud-connections` (CRUD, `setup`, `test`, `tests/{id}`).
- **Uses:** inventory only; the wizard shows cost and posture as "later" and the API refuses them.
- **Access methods:** AWS `probe`, `role` (external ID generated by the control plane: a new
  connection takes the one the wizard's `setup` issued, which carries the control plane's MAC, and
  any other value is replaced; an update keeps the connection's own) and
  `federated`; Azure `probe` (managed identity, client ID optional), `workload` (a user-assigned
  identity trusting the probe's Kubernetes service account through `AZURE_FEDERATED_TOKEN_FILE`) and
  `federated`.
- **Federation key:** Microsoft Entra supports only RS256 for external issuers, so federated tokens
  are signed with their own RSA key (`cloud_federation_keys`), published in the same
  `/.well-known/jwks.json` beside the ES256 keys. Sites accept only ES256, so these tokens can't be
  replayed against a Site. The control plane serves `/.well-known/openid-configuration`; tokens are
  `sub = probe:<group ID>`, the provider's audience, 10 minutes, `iss = PUBLIC_URL`.
- **Probe protocol:** connections arrive in the probe config (`cloud`), scheduled like monitors on
  one live member of the group (rendezvous over `cloud:<id>`). Each scan posts its outcome to
  `POST /api/v1/synthetics/probe/cloud/{id}/scan`; Test access is a probe task answered on
  `POST /api/v1/synthetics/probe/tasks/{id}/cloud-result`; federated tokens come from
  `POST /api/v1/synthetics/probe/cloud/{id}/token`, and for Test access of an unsaved federated
  connection from `POST /api/v1/synthetics/probe/tasks/{id}/cloud-token` (only the group's own
  task, until its result arrives). The probe reuses a token until shortly before it expires.
- **Who sees what:** admins see a connection whole; other users see what it scans and how that
  went, without the role or client ID, external ID and tenant ID.
- **Discovery** (`internal/cloud/discovery`):
  - AWS uses the SDK: EC2, ELBv2 with target health, EKS, RDS, Auto Scaling, Lambda and S3. For an
    Organization it lists the accounts with the management account's role, then assumes the role
    of the same name in each (up to 200 accounts), four accounts and four regions per account at
    a time. An account without the role is listed in the scan's errors; the rest are scanned.
    `tag:GetResources` breadth is not built yet.
  - Azure is plain REST: a token from IMDS or Entra (client assertion), then one Resource Graph
    query, paged.
  - A denied call marks the use "needs permission" with the missing action; the rest is still
    scanned.
- **Two scans per connection** (both admin-adjustable in the wizard):
  - **Full discovery**, every 4 hours by default (30 min to 24 h): lists everything. New
    resources appear only after one.
  - **Health scan**, every 15 minutes by default (5 to 60 min, never less often than discovery):
    re-reads only what the last discovery found, and only in the accounts, regions and types where
    it found something. AWS: instance state and status checks, asked for by the known instance
    IDs (up to 1,000 per region; more lists the region), load balancer state with target health
    on every third health scan (every one while a target is unhealthy), EKS, RDS and Auto Scaling
    state. Health scans reuse the account sign-ins of the last discovery, whose credentials
    refresh themselves, instead of assuming roles and listing an Organization again. Azure: the
    same Resource
    Graph query limited to the known types, plus Resource Health availability
    (`HealthResources`). Buckets and functions keep what discovery found. A resource that is
    gone is no longer sent, so the Site sees it stop reporting.
  - One health scan runs straight after each discovery, and only health scans send to the Site,
    so every resource carries its latest health (`cloud_health`: `healthy`, `degraded`,
    `unhealthy`, `unknown`). The probe keeps the latest inventory on disk
    (`<data dir>/cloud/<id>.json`, rewritten only when it changed), so a restart waits for the
    next discovery instead of starting one; a member that takes a connection over discovers first.
    One connection keeps at most 50,000 resources (AWS and Azure alike).
- **Throttling:** AWS and Azure throttle per account (or identity) and region, so:
  - calls are paced per endpoint (10 per second per account and API host, 3 per second for
    Resource Graph, which allows about 15 per 5 seconds);
  - when a cloud says how long to wait (`Retry-After`, `x-ms-retry-after-ms`, or Resource Graph's
    `x-ms-user-quota-resets-after` once `x-ms-user-quota-remaining` reaches 0), that endpoint
    stands down for as long (up to 5 minutes). An Azure call is then retried, up to 4 times; an
    AWS call is left to the SDK's own retries, since retrying in both would multiply the attempts;
  - the AWS SDK runs in adaptive retry mode with 8 attempts and backoff up to 30 s, so throttling
    errors without a wait slow its own rate;
  - each scan reports how many calls were throttled and how long it waited; Settings → Cloud
    connections shows it on the status.
- **Time limits:** a discovery may run for 80% of its interval (5 min to 2 h), a health scan for
  80% of its own (2 to 30 min), Test access for 100 s. A scan that hits its limit still sends what
  it found (on a fresh context), is reported as partial ("error", with a hint to scan less often or
  narrow the scope), and a partial discovery keeps the resources the last one found that it didn't
  reach.
- **Delivery:** metrics, not log records: `cloud_resource_info` (value 1, labels
  `cloud_provider`, `cloud_account_id`, `cloud_region`, `cloud_resource_type`, `cloud_resource_id`,
  `cloud_resource_name`, `cloud_resource_state` and attributes such as `host_id`,
  `k8s_cluster_name`, `instance_type`) and `cloud_lb_targets{health}`. The builder reads them like
  other anchor metrics.
- **Merging:** an EC2 instance or Azure VM joins the agent host with the same `host_id`; an
  EKS or AKS cluster joins the cluster of the same name; everything else is a new asset of kind
  `load_balancer`, `database`, `bucket`, `function` or `scaling_group`. A resource only the cloud
  reports is "not reporting" after three of its connection's health scans plus 5 minutes
  (`cloud_refresh_seconds` on each series; 50 minutes at the default 15, never less), instead of
  10 minutes. Health comes from the cloud's state (stopped is inactive), `cloud_health`
  (`unhealthy` is critical, `degraded` a warning), unhealthy load balancer targets and scale sets
  below their desired size.
- **Templates:** `deploy/cloud/aws/obzly-readonly-role.yaml` (one CloudFormation template for role
  and federated access, linted with cfn-lint) and `deploy/cloud/azure/obzly-reader.bicep` with
  `obzly-identity.bicep` (built with the Bicep CLI). The wizard offers the same files with a deploy
  command carrying the connection's values, or a CLI script. For the probe's own AWS role it
  generates a policy with exactly the calls discovery makes, since CloudFormation can't attach AWS
  managed policies to an existing role.
- **The probe's own identity on Kubernetes:** the `obzly-probe` chart now creates a service account;
  set `serviceAccount.annotations` for IRSA (`eks.amazonaws.com/role-arn`) or Azure workload identity
  (`azure.workload.identity/client-id`, with `podLabels: {azure.workload.identity/use: "true"}`).
  EKS Pod Identity needs only an association with that account.
- **Not verified against real AWS or Azure accounts** in this release's tests: discovery is tested
  against local fakes of STS, EC2, RDS, Entra and Resource Graph.

## Implementation notes (v0.55.0)

Where the build differs from, or adds to, the design above:

- **Discovery** reads the series of a few anchor metrics per asset with their first and last seen
  (`metrics_core.ListSeriesLifetimes`). The series stream answers once it covers the last hour, and
  then first seen reaches back 30 days. A project whose series tracking started within the hour
  falls back to its samples, so first seen starts at the first build. Values come from about 30
  instant PromQL queries per project per build.
- **Cluster name:** not detected automatically. The EKS detector can only read it with
  `ec2:DescribeTags`, which agent roles usually lack. Data without `k8s_cluster_name` is grouped as
  "(unnamed cluster)"; set the name in the manifest when a project has several clusters.
- **Workloads** come from the workload labels `k8s_attributes` now adds to metrics. Without them
  (an older manifest), only Deployment pods (`<name>-<hash>-<suffix>`) and StatefulSet pods
  (`<name>-<n>`) are grouped. DaemonSet and Job pods then sit directly under their namespace.
- **Pods and containers:**
  - CPU and memory are a share of the limit when there is one (the `*_limit_utilization`
    metrics), otherwise of the node; the table says which on hover. The Kubernetes manifest
    collects those metrics only with "Limit utilization metrics" ticked: they need the
    `nodes/proxy` permission, which also reaches the kubelet's exec API.
  - "Crash looping" is 10 or more restarts in an hour. The `k8s.container.status.reason` metric is
    still experimental in the collector.
- **Error-log share** groups the last 15 minutes of logs by pod name.
- **API:**
  - `GET /inventory/assets` returns at most 5000 assets per Site, worst health first. The tab says
    when a Site has more.
  - Filtering, sorting and the tree view happen in the browser across the merged Sites.
- **Standalone Sites** show the tab too, with their own inventory.
- **Icons:** the official Kubernetes, AWS and Azure icons (credited in `NOTICE`).

## Phase 1 scope (v0.55.0)

- **In:** the asset kinds above from OTel data, lifecycle and change feed, health rules with
  reasons and alert links, the Site API, the cross-Site tab (overview, assets with tree view and
  header sort, detail fly-out), and the manifest changes.
- **Out:** cloud connections and cloud inventory (v0.56.0), cost and posture uses (later), per-project threshold overrides, inventory-based alert rules
  beyond the existing alert links, and a CMDB-style export.
