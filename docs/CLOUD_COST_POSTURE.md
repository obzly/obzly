# Cloud cost and security posture

Status: approved 2026-11-09 with its UI mock; built and shipped in v1.1.0, in three milestones (see
[Build phases](#build-phases)). It adds the **cost** and **posture** uses to the cloud connections
from [`INFRASTRUCTURE.md`](INFRASTRUCTURE.md).

## Goal

For the AWS accounts and Azure subscriptions Obzly already reads for inventory:

- **Cost:** what each account, service, region, resource and team spends, by day, month, quarter or
  year; forecasts, budgets and anomalies.
- **Posture:** misconfigurations measured against CIS, AWS Foundational Security Best Practices
  (FSBP) and the Microsoft cloud security benchmark (MCSB), with history and coverage.
- **Recommendations:** savings from rightsizing, idle resources and commitments (Savings Plans,
  Reserved Instances, Azure reservations and savings plans), and a ranked plan to improve posture.

Obzly stays read-only: it never changes a resource and never buys a commitment.

## Rules that keep cost, inventory and posture consistent

1. **One registry, one identity.**
   - Cloud connections are the only list of accounts and subscriptions; there is no separate
     "billing accounts" list.
   - Every cloud resource has one key: its AWS ARN, or its Azure resource ID lowercased at ingest.
   - The same key joins the inventory asset, its cost rows (FOCUS `ResourceId`) and its findings
     (OCSF `resources[].uid`, Defender `resourceId`). The asset drawer shows all three.
2. **Cost is read once per billing scope.**
   - A connection to an AWS management account or an Azure billing scope reads the billing data for
     all its members.
   - A member account's connection shows "Cost via Contoso-Payer" and never reads cost itself, so
     nothing is counted twice.
3. **One source per day, and the source is always shown.**
   - For each connection and day, cost comes from either the export or the API, never both. In
     Automatic mode, export rows replace API rows for every day the export covers.
   - API rows are mapped into the same FOCUS columns, so screens don't change with the source.
   - Every figure carries:
     - Billed or Effective (amortized);
     - its currency: totals are never added across currencies, never converted, and shown per
       currency;
     - its source;
     - its freshness. Days the provider hasn't finalised are marked estimated.
4. **One posture source per connection, region and standard:** native (Security Hub or Defender for
   Cloud), the Obzly baseline, or "not covered". A finding never appears twice. The Coverage view
   says why each fallback happened.
5. **Unknown is never zero or pass.**
   - Each use and section reports OK, Stale, Throttled, Needs permission (naming the missing
     action) or Not enabled.
   - A failed section keeps its last good data and is marked stale.
   - Periods a source can't provide show "n/a", never 0:
     - cost older than the API keeps;
     - single-resource cost older than 14 days;
     - posture before it was turned on.
   - Findings resolve only in scopes that were scanned successfully.
6. **Findings have a lifecycle:** open → resolved → reopened. Suppressing one needs a reason and can
   have an expiry date, after which it reopens. Every change is kept as history.

## Where things run

| Part | Runs on | Does |
| --- | --- | --- |
| Cloud connection, wizard, templates | Control plane | Uses, data-source choices, permissions per use, Test access per use |
| Collection | `obzly-probe` (the connection's probe group) | Reads exports and APIs with the connection's identity, runs baseline checks, sends results |
| Storage, lifecycle, recommendations | The Site that owns the connection's destination project | Builds tables from the probe's batches, runs anomalies and recommendations, evaluates budgets |
| Cost and Posture tabs | Control plane | Fan out to Sites through the proxy, like Infrastructure; merge per currency |
| Saved views | Control plane | Per user, optionally shared with everyone |

- **Delivery:** the probe posts each batch to the destination Site's `POST /v1/cloud/cost`
  (gzip JSON), authenticated with the destination project's API key like its other ingest. It
  goes straight to the Site, never through the control plane. Inventory keeps sending metrics;
  cost rows aren't telemetry, so they get their own receiver rather than riding on log records.
- **Batches:** each delivery is a batch with an ID, the days it covers and chunks of at most
  20,000 rows, each numbered `seq` of `total`. The Site stages chunks and applies a batch only
  once all of them have arrived, replacing that connection's and source's rows for those days in
  one transaction. A half-delivered batch changes nothing, and resending a chunk is harmless.
  For a day an export covers, the export's rows replace the API's.
- **Data volume:** daily cost rows at resource level for a few thousand resources are a few
  million rows a year. That fits Postgres with monthly partitions. Moving them to the segment store
  is an open question for very large estates.

## Cost

### Data source, chosen per connection

| | Automatic (default) | Export only | API only |
| --- | --- | --- | --- |
| How | FOCUS export once it lands; the billing API fills in until then, and if the export stops | FOCUS export | AWS Cost Explorer API / Azure Cost Management query API |
| Cost to you | Export storage, plus a few API calls at the start | Export storage, usually cents a month | AWS: $0.01 per request (each page counts), plus a per-usage-record charge for hourly or resource-level data. Azure: free, but throttled per tenant |
| Resource level | Yes | Yes | No: Obzly reads the APIs by account, service and region (AWS has resource level for 14 days only, as a paid opt-in) |
| Amortized (effective) cost | Yes | Yes | AWS: `AmortizedCost` metric. Azure: `AmortizedCost` type |
| History | From the export's start; AWS and Azure can backfill | Same | AWS: 13 months (38 months at monthly granularity with multi-year data on). Azure: per query range |
| Setup | Bucket or storage account, and the export | Same | None beyond the role |

The wizard shows these as cards with a live monthly estimate. For Cost Explorer it's the refresh
rate times one request per account in scope plus one forecast, at $0.01 each, with the one-off
90-day backfill noted beside it. Obzly reads Cost Explorer by account, service and region only, so
it never turns on (or pays for) resource-level API data; single resources come from the export.

The source can be changed later without losing history.

### FOCUS as the one schema

- **AWS:** Data Exports with the **FOCUS 1.2** table. FOCUS 1.0 is hourly only; 1.2 adds daily and
  monthly granularity. Daily is the default; hourly only where the commitment planner needs it (see
  below). Parquet to S3, delivered daily.
- **Azure:** Cost Management exports of type `FocusCost` ("Cost and usage details (FOCUS)") to a
  storage account, daily, Parquet. Backfill: up to 13 months from the portal, longer through the
  REST API.
- **Columns kept:**
  - when and who: `ChargePeriodStart`, `BillingAccountId`, `SubAccountId`;
  - what: `ServiceName`, `RegionId`, `ResourceId`, `ChargeCategory`, `PricingCategory`;
  - money: `BillingCurrency`, `BilledCost`, `EffectiveCost`, `ListCost`;
  - commitments: `CommitmentDiscountId`;
  - tags: only the tracked tag keys, never all of them.
- **Grain:** the probe aggregates the export to one row per day and per those dimensions before
  sending. Raw line items stay in the customer's bucket.
- **API mapping:**
  - Cost Explorer `UnblendedCost` → BilledCost, `AmortizedCost` → EffectiveCost;
  - `LINKED_ACCOUNT` → SubAccountId;
  - Azure `ActualCost` / `AmortizedCost` → the same.

### Collection

- **Exports:**
  - The probe lists the export prefix and reads each billing period's files.
  - A provider rewrites a month's files when it restates them, so a new file set for a period
    replaces that period's rows.
  - The current and previous month are reread daily; older months only when their files change.
- **API:**
  - after a one-off backfill, only the last 5 days are reread, because restatements happen there;
  - payer accounts are filtered by `LINKED_ACCOUNT` per member;
  - Cost Explorer's `End` date is exclusive;
  - forecast = month-to-date actual + the provider's forecast for the remaining days only.
- **Rate limits:**
  - one cost refresh at a time per payer or tenant, because limits are shared there;
  - provider retry-after headers are obeyed;
  - Azure POST queries need retries switched on per request;
  - a 429 marks the section Throttled, never "0 rows".
- **Recommendations, commitments and purchase options** are read weekly, after a cost refresh, by
  the connection that reads cost, to keep API charges down:
  - AWS: about 75 Cost Explorer requests a week (about $3.20 a month, shown in the wizard), plus
    Compute Optimizer and EC2 describe calls, which are free;
  - Azure: Advisor, reservation and savings plan recommendations, free but throttled.
  - They go to the Site's own `POST /v1/cloud/advice`, chunked like cost and posture batches.
  - Each part (rightsizing, Obzly checks, commitments, planner) reports ok, not enabled, needs
    permission or error. A part that isn't ok keeps its last good data on the Site.

### On the Site

- **Tables:**
  - `cloud_cost_daily`: partitioned by month. Keys: connection, source, day, billing account,
    sub-account, service, region, resource key, charge and pricing category, currency. Amounts:
    billed, effective, list. Also tracked tags and an `estimated` flag.
  - `cloud_cost_sources`: per connection, source and period: last file or call, rows, final or
    estimated, and section status.
- **Money:** stored as `NUMERIC(20,6)`, never floating point.
- **Tracked tags:** the keys to group and budget by. They are set on the control plane and sent to
  probes in the connection config.

### Views

- **Overview:**
  - month to date, forecast and last month, each per currency;
  - pace against last month;
  - budget status;
  - daily trend with estimated days dashed and anomalies marked;
  - top services, accounts and tags;
  - a data-status strip (source, freshness, stale sections).
- **Explorer:**
  - **Range:** presets (this month, last 30 days, last month, this quarter, last quarter, last 6
    months, year to date, last 12 months, last year, all history) or custom dates.
  - **Period:** daily (ranges up to 92 days), monthly, quarterly or yearly. Cut-short periods are
    labelled "to date" or with their dates.
  - **Filters:** provider, account, region, service, resource, tracked tags. Each is a multi-select
    that only offers values matching the other filters, so picking Azure leaves only Azure values,
    with "For Azure · Show all providers" to lift it. Selections that stop matching are removed, with
    a note.
  - **Group by:** any filter dimension, or charge category.
  - **Display:** a stacked chart (top 7 + Other) and a table with one column per period, a total and
    a share, one per currency.
  - **Drill-down:** click a row to filter to it and go one level deeper; resource rows open the
    asset. Copy as CSV.
- **Saved views** (Explorer and Posture Trends):
  - named, private or shared with everyone, read-only to others, who can save a copy;
  - preset ranges stay relative; custom ranges keep their dates;
  - "Edited" shows with Update and Revert; one view per tab can be the user's default.

### Budgets and anomalies

- **Budgets:**
  - monthly, per connection, account or tracked tag, in one currency;
  - checked against the month-end forecast;
  - alerts at a threshold and when over, through managed Site alert rules (as AI budgets do) to
    shared channels;
  - an alert fires when the status changes, not on every refresh.
- **Anomalies:**
  - per service and per account total;
  - each day is compared with the median of the same weekday over the previous 8 weeks, using a
    robust z-score (median absolute deviation);
  - flagged when z ≥ 3.5 **and** the change is at least 10% **and** at least 50 in the account's
    currency;
  - a drop on an estimated day is never flagged;
  - recomputed only when the cost section refreshed successfully;
  - each alert says why it was flagged.

## Posture

### Source, chosen per connection

| | Automatic (default) | Native only | Obzly baseline only |
| --- | --- | --- | --- |
| How | Native where it is on and readable, the baseline elsewhere | Security Hub / Defender for Cloud | Obzly's own checks |
| Cost to you | Native charges where native is used | AWS: about $0.001 per check (cheaper per check above 100,000 checks), plus AWS Config. Azure: foundational CSPM is free; the Defender CSPM plan is billed per server, storage account, database and serverless resource | No service charge; read-only API calls |
| Coverage | Full where native, baseline elsewhere | Regions or subscriptions without it are not covered | The baseline set, smaller than native |

### Native findings

- **AWS Security Hub:**
  - read with `GetFindings` (ASFF, generally available) per region: `DescribeHub` says whether it's
    on, `GetEnabledStandards` which standards are; `GetFindingsV2` (OCSF) stays an option once it
    leaves preview;
  - filtered to Security Hub's own control findings (`ProductName` Security Hub, active, passed or
    failed), so passed controls count toward scores too;
  - a finding's control is its consolidated `SecurityControlId` (e.g. `S3.8`); CIS numbers come
    from its related requirements ("CIS AWS Foundations Benchmark v3.0.0/2.1.4");
  - only FSBP and CIS AWS are mapped; other enabled standards are left out.
- **Azure:** one Resource Graph query over `securityresources` (Defender assessments, healthy and
  unhealthy), mapped to the Microsoft cloud security benchmark, which Defender assesses by default.
  A subscription with no assessments counts as "not enabled". Azure scopes are whole subscriptions.
- **Detection:** the probe records per region (AWS) or subscription (Azure) and standard whether
  native is on and readable, which feeds the Coverage view and Automatic mode.
- **Account-wide controls** (IAM, CloudTrail, S3 account settings) come from Security Hub wherever
  it runs; the baseline reads them only when no region of the account has it.

### Obzly baseline

- **What it is:** a catalog of checks written in **CEL** (the expression language Kubernetes uses
  for admission policies), one file per check.
- **What a check holds:**
  - the resource type;
  - the CEL expression;
  - severity;
  - the standards and controls it maps to (structured `{standard, version, control}`, not free
    text);
  - remediation as CLI, IaC and console steps;
  - pass and fail fixtures, tested in CI.
- **Where it runs:** in the probe, against the configuration it already reads for inventory, plus
  the extra calls `SecurityAudit` allows.
- **What leaves the probe:** only the results: finding, evidence fields and resource key. Raw
  configuration never does.
- **Collect less:** sensitive values (user data, environment variables, parameters) are never
  collected. A check that needs them looks at a yes/no fact (e.g. "has environment variables").
- **Size:** v1.1.0 starts with 19 checks (12 AWS, 7 Azure) on the most cited controls: public
  access, admin ports open to the internet, encryption, the root user, CloudTrail and IMDSv2. The
  catalog grows release by release towards about 120.
- **Identity:** an AWS check uses the FSBP control it stands in for (e.g. `EC2.8`) as its control,
  so a finding keeps its history when Security Hub is turned on or off; an Azure check uses its own
  ID, as Defender's assessments have no shared control ID.
- **Unknown is never a pass:** a fact the probe couldn't read fails the check with the error as
  evidence, and a scope whose reads failed resolves nothing.
- **Why CEL and not Rego:** it is small, side-effect free and fast enough to embed in the probe, and
  the people who write the checks already know it from Kubernetes.

### Findings

- **Delivery:** the probe posts each refresh to the Site's `POST /v1/cloud/posture` with the
  project's API key, in chunks of up to 5,000 findings; the Site applies it once every chunk has
  arrived, like cost.
- **`posture_findings`:** one row per connection, control and resource key (the source is a column,
  so it can change). It holds:
  - severity and status;
  - first seen, last seen, resolved at;
  - suppression: reason, until, by;
  - evidence (small);
  - the standards and controls.
- **Resolving:** a finding missing from a refresh resolves only if an ok run of its scope came from
  its own source, or from a source that evaluated its control. A source that doesn't evaluate the
  control, or a scope that wasn't read, leaves it open.
- **`posture_finding_events`:** opened, resolved, reopened, suppressed, unsuppressed. Used by Trends
  and history.
- **`posture_scope_runs`:** per connection, region, standard and source: result and reason. A
  finding resolves only when a later run of its scope succeeds without it.
- **When the source changes** (for example Security Hub turned on in a region), findings carry over
  by resource and control, keeping their history.
- **Scores:** per standard, passed ÷ evaluated controls, like the native consoles. Suppressed
  findings are left out; unknown is never counted as a pass.

### Views

- **Standards:** a score per standard with its source and a trend.
- **Findings:**
  - severity, resource (linking to the asset), account and region, status, first seen, source;
  - the drawer holds evidence, remediation, suppress with a reason and expiry, and history.
- **Trends:**
  - the same ranges, periods, faceted filters and saved views as the cost Explorer, plus severity,
    standard and source;
  - measures: open findings at period end, new vs resolved, standard scores.
- **Coverage:** a connection × region grid (native, baseline, not covered, not in scope) with the
  reason for each.
- **Alerts:** a new or reopened critical or high finding, and a posture section turning Stale or
  Needs permission, sent to shared channels.

## Recommendations

### Cost

- **Kinds:** rightsizing, idle (stopped or unattached resources, idle GPUs), modernization (gp2 →
  gp3, Graviton) and storage (lifecycle rules).
- **Sources:**
  - **Compute Optimizer:** EC2 rightsizing (overprovisioned instances), EBS volumes (for example
    gp2 to gp3) and idle resources. It needs an opt-in (free); without it the section says "not
    enabled".
  - **Azure Advisor**, cost category. Its purchase advice is left to the planner.
  - **Obzly checks:** unattached EBS volumes and managed disks, gp2 volumes, unused Elastic IPs and
    public IP addresses. On Azure they need Reader (Inventory or Posture on the connection), because
    Resource Graph without it answers "nothing", which would read as "nothing idle".
  - Where both have one for the same resource and kind, the provider's recommendation wins.
  - **Obzly agent metrics:** CPU and memory p95 over 14 days for EC2 rightsizing, joined through
    `host_id` (the instance ID). CloudWatch alone has no memory figure. A busy memory (p95 of 80%
    or more) marks the recommendation high risk.
- **Savings:** the provider's estimate. An Obzly check has no estimate: its saving is a share of
  the resource's own effective cost over the last 30 days (all of it when idle, 20% for gp2 to
  gp3), and is "n/a" until an export has resource-level cost. Elastic IPs use the list price
  ($0.005 an hour).
- **Storage:**
  - `cost_recommendations` is replaced on each refresh, per section that came back ok;
  - `cost_recommendation_states` keeps open, planned (owner, due date), done and dismissed (reason)
    by a stable key, so state survives refreshes.
- **Done is detected** when a recommendation disappears from a refresh whose section read ok. If it
  comes back later, the change didn't stick and it reopens; one someone marked done stays done.
- **Realized savings:** the median daily cost 14 days before vs 14 days after, shown against what
  was expected, measured 16 days after the change so late cost data has arrived. Without at least 7
  days on each side it stays n/a.

### Commitments

- **Existing commitments:** Savings Plans, EC2 and RDS Reserved Instances, and Azure reservations
  and savings plans, with coverage, how much each is used, and when it ends.
  - AWS reads them from Cost Explorer's utilization and coverage reports, so no per-region describe
    calls are needed.
  - Azure reads the reservations and savings plans the identity can see. That needs Reservations
    Reader (assigned at `/providers/Microsoft.Capacity`), which a template can't assign; the setup
    script shows the command, and until then the section says "needs permission".
  - A commitment used 0% (for example an RDS reservation for a family that was migrated) is flagged.
  - So is one ending within 60 days; that also alerts, on the project's cost alert channels.
- **Planner:**
  - **Types:**
    - AWS: Compute Savings Plan, EC2 Instance Savings Plan, EC2 and RDS Reserved Instances;
    - Azure: savings plan, reservations.
  - **Options compared:** 1 year vs 3 years against No / Partial / All upfront on AWS, or Monthly /
    Upfront on Azure (same price there).
  - **Look-back:** 7, 30 or 60 days.
  - **Numbers come from the providers' own purchase recommendations:**
    - Cost Explorer `GetSavingsPlansPurchaseRecommendation` and
      `GetReservationPurchaseRecommendation`;
    - the Azure reservation and savings plan recommendation APIs.
  - **Combinations not offered** show "Not offered". For example, AWS doesn't sell 3-year RDS
    reservations without upfront, and some newer RDS families have no 3-year term.
  - **What Obzly adds:**
    - **Rightsize first** (on by default): eligible usage is reduced by the open rightsizing
      savings, so the planner and the rightsizing list never count the same saving twice;
    - "Lowest risk" and "Most savings" labels;
    - payback month;
    - a usage chart.
  - **The coverage slider** (cover 50–120% of steady usage, with unused hours shown) needs hourly
    data, and FOCUS 1.2 exports are daily, so it isn't built yet: the provider's recommended
    commitment is used.
  - **How to buy** lists the console steps. Obzly never buys.

### Posture

- **Actions:** each groups the findings that one change fixes. Examples:
  - "Lock down contoso-logs-eu: require TLS and turn on access logging" closes two findings;
  - "Delete 6 unattached, unencrypted EBS volumes" is linked to the matching cost recommendation.
- **Guardrails** stop a finding coming back. A guardrail that is itself a control (EBS encryption
  by default is EC2.7) also closes that finding, and its separate fix isn't offered as well:
  - S3 Block Public Access for the whole account;
  - IMDSv2 as the default for new instances;
  - EBS encryption by default;
  - an Azure Policy that denies open management ports.
- **Coverage actions** in the scopes that fall back or aren't covered:
  - turn on Security Hub in a region;
  - add the posture permissions to a connection's role;
  - assign Security Reader.
- **Ranking:** severity closed per unit of effort, coverage gaps first.
- **Plan:** actions can be added to a plan; the standard cards show the score with the plan done,
  and with everything done.
- **Status:**
  - Done when its findings resolve;
  - "Accepted risk", not Done, when they were suppressed instead.

## Connection wizard and settings

- **Uses:** Cost and Posture are enabled on an existing connection. Adding them produces an update
  to the same CloudFormation stack, Bicep deployment or CLI script, never a second connection.
- **Cost step:**
  - billing scope: this account, or the management account / billing scope for all members;
  - data source cards (Automatic, Export only, API only) with live estimates;
  - the export's bucket and prefix, or storage account and container;
  - a generated export definition (AWS Data Exports FOCUS 1.2; Azure `FocusCost`).
- **Posture step:**
  - what the probe found per region (native on or off, standards enabled);
  - source cards (Automatic, Native only, Baseline only) with their costs.
- **Test access** runs per use: export readable and last file, Cost Explorer reachable, Security
  Hub readable per region.
- **Settings:**
  - tracked cost tags;
  - budgets;
  - alert routing to shared channels for anomalies, budgets, new critical or high findings, and
    data problems.

## Permissions per use

Every permission is read-only. The cost use reads only the export prefix, never other data in the
bucket or storage account.

| Use | AWS | Azure |
| --- | --- | --- |
| Cost: export | `s3:GetObject` and `s3:ListBucket` on the export prefix, `bcm-data-exports:GetExport` / `ListExports` | `Storage Blob Data Reader` on the export container |
| Cost: API, forecasts and recommendations | `ce:GetCostAndUsage`, `GetCostForecast`, `GetDimensionValues`, `GetTags`, `GetSavingsPlansPurchaseRecommendation`, `GetReservationPurchaseRecommendation`, `GetSavingsPlansCoverage`, `GetSavingsPlansUtilizationDetails`, `GetReservationCoverage`, `GetReservationUtilization`; `compute-optimizer:GetEnrollmentStatus`, `GetEC2InstanceRecommendations`, `GetEBSVolumeRecommendations`, `GetIdleRecommendations`; `ec2:DescribeRegions`, `DescribeInstances`, `DescribeVolumes`, `DescribeAddresses` (Obzly checks) | `Cost Management Reader` (and `Billing Reader` at billing scope), which also reads Advisor; `Reservations Reader` for existing reservations and savings plans (optional, tenant level); `Reader` for the Obzly checks |
| Posture | `SecurityAudit` (baseline), `securityhub:DescribeHub`, `GetEnabledStandards`, `GetFindings` | `Security Reader`, and `Reader` for the baseline checks' resource reads |

This replaces the cost and posture rows of the table in `INFRASTRUCTURE.md`.

## APIs

New Site routes are served through the control plane's `/sites/{id}` proxy and added to its
allowlist (see the `site-scoped-api` checklist). Admins and project members can read; changing a
finding, recommendation or budget needs a project member.

- **Cost:**
  - `/api/v1/cloud-cost/summary`, `/explore` (range, period, group, filters, metric, currency),
    `/facets`, `/status`;
  - `/budgets`, `/anomalies`;
  - `/recommendations` (and `PATCH` with the project and key in the body, since keys are ARNs),
    `/resource-recommendations` (the asset drawer), `/commitments`, `/planner` (look-back,
    rightsizing first);
  - receiver: `POST /v1/cloud/advice`.
- **Posture:**
  - `/api/v1/posture/standards`, `/findings`, `PATCH /findings/{id}` (suppress or unsuppress);
  - `/trends`, `/coverage`;
  - `/actions` (and `PATCH` for plan or dismiss).
- **Control plane:** `/api/v1/saved-views` (CRUD; a shared view is read-only to everyone but its
  owner), plus each user's default view per tab.
- **MCP:** read-only tools `cost_summary`, `cost_explore`, `cost_anomalies`, `cost_budgets`,
  `cost_recommendations` (with flagged commitments), `posture_scores`, `posture_findings` and
  `posture_coverage`. Each is scoped to the caller's projects, and resource names and tags
  are marked as data, not instructions.

## Security and privacy

- **Credentials:** the same as inventory. The probe's own identity, a role with an external ID, or
  federation through the control plane. Obzly stores only identifiers.
- **Raw billing line items** stay in the customer's bucket. Only daily aggregates and tracked tags
  are sent.
- **Baseline checks** run in the probe; only findings and minimal evidence leave it.
- **Audit:** suppressions, dismissals, plan changes and shared views are written to the audit log.

## Build phases

Everything ships together in **one release, v1.1.0**: one branch, one pull request, one `FEATURE`
commit and one tag, so operators upgrade once. The work is done in this order, and each milestone is
finished and tested before the next starts:

1. **Cost:**
   - the cost use with source choice and billing scope;
   - FOCUS 1.2 export readers (AWS, Azure) and the API fallback;
   - Site tables;
   - Overview and Explorer with saved views;
   - budgets (managed rules) and anomalies;
   - the asset drawer's cost section.
2. **Posture:**
   - native ingestion (Security Hub, Defender for Cloud);
   - the findings lifecycle and scope runs;
   - Standards, Findings, Trends and Coverage;
   - the first baseline checks;
   - alerts for new critical and high findings.
3. **Recommendations:**
   - cost recommendations with states and realized savings;
   - commitments and the planner;
   - posture actions, guardrails and the score simulator;
   - MCP tools.

The release also updates:
- the role and Bicep templates for all new permissions, so a connection is updated once;
- the docs and the upgrade runbook;
- `docs/releases/v1.1.0.md`.

## Open questions

- Moving `cloud_cost_daily` to the segment store for estates with hundreds of thousands of
  resources.
- Which Azure agreement types support `FocusCost` exports; the API fallback covers the rest.
- Security Hub `GetFindingsV2` is in public preview; `GetFindings` (ASFF) stays as the fallback
  until it is generally available.
- The size of the first baseline catalog, and who reviews new checks.

## References

- AWS Cost Explorer pricing and API best practices:
  <https://aws.amazon.com/aws-cost-management/aws-cost-explorer/pricing/>,
  <https://docs.aws.amazon.com/cost-management/latest/userguide/ce-api-best-practices.html>
- Cost Explorer multi-year data:
  <https://docs.aws.amazon.com/cost-management/latest/userguide/ce-multi-year-data.html>
- AWS Data Exports for FOCUS 1.2:
  <https://aws.amazon.com/blogs/aws-cloud-financial-management/data-exports-for-focus-1-2-is-now-generally-available>
- Azure FOCUS exports: <https://focus.finops.org/get-started/microsoft/>
- Security Hub CSPM pricing: <https://aws.amazon.com/security-hub/cspm/pricing>
- Security Hub `GetFindingsV2`:
  <https://docs.aws.amazon.com/securityhub/1.0/APIReference/API_GetFindingsV2.html>
- Defender for Cloud pricing:
  <https://azure.microsoft.com/pricing/details/defender-for-cloud>
- RDS reserved DB instances (payment options):
  <https://docs.aws.amazon.com/AmazonRDS/latest/UserGuide/USER_WorkingWithReservedDBInstances.html>
- Azure reservation exchanges and refunds:
  <https://learn.microsoft.com/en-us/azure/cost-management-billing/reservations/exchange-and-refund-azure-reservations>
