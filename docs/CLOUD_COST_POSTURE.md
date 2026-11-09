# Cloud cost and security posture (design)

Status: approved 2026-11-09 with its UI mock. Not built yet; the build is split into three releases
(see [Build phases](#build-phases)). It adds the **cost** and **posture** uses to the cloud
connections from [`INFRASTRUCTURE.md`](INFRASTRUCTURE.md), which the wizard already lists as "later".

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

- **Delivery:** the probe sends results as OTLP log records to the connection's destination project,
  like cloud inventory. This reuses the project API key, the ingest pipeline and the proxy, with no
  new endpoint.
- **Batches:** each delivery is a batch with an ID, the scope it covers and a closing manifest
  record with its row count. The Site applies a batch only once the manifest has arrived and the
  count matches, and it replaces the rows for that scope in one transaction. A half-delivered batch
  changes nothing.
- **Data volume:** daily cost rows at resource level for a few thousand resources are a few
  million rows a year. That fits Postgres with monthly partitions. Moving them to the segment store
  is an open question for very large estates.

## Cost

### Data source, chosen per connection

| | Automatic (default) | Export only | API only |
| --- | --- | --- | --- |
| How | FOCUS export once it lands; the billing API fills in until then, and if the export stops | FOCUS export | AWS Cost Explorer API / Azure Cost Management query API |
| Cost to you | Export storage, plus a few API calls at the start | Export storage, usually cents a month | AWS: $0.01 per request (each page counts), plus a per-usage-record charge for hourly or resource-level data. Azure: free, but throttled per tenant |
| Resource level | Yes | Yes | AWS: last 14 days only, after opting in. Azure: yes |
| Amortized (effective) cost | Yes | Yes | AWS: `AmortizedCost` metric. Azure: `AmortizedCost` type |
| History | From the export's start; AWS and Azure can backfill | Same | AWS: 13 months (38 months at monthly granularity with multi-year data on). Azure: per query range |
| Setup | Bucket or storage account, and the export | Same | None beyond the role |

The wizard shows these as cards with a live monthly estimate. It's calculated from:
- the refresh rate;
- the number of accounts;
- the number of tracked tags;
- whether resource-level API data is on.

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
- **Purchase and rightsizing recommendations** from Cost Explorer, Compute Optimizer and Azure
  Advisor are fetched weekly, to keep API charges down.

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
  - read in OCSF with `GetFindingsV2`, or with `GetFindings` in ASFF where V2 isn't available, both
    mapped to one finding model;
  - filtered to Security Hub's own control findings;
  - a finding's control ID (e.g. `S3.8`) and the standards that cite it come from the control
    definitions;
  - IAM: `securityhub:GetFindings` covers both calls.
- **Azure:** one Resource Graph query over `securityresources` (assessments and their statuses),
  like the inventory query.
- **Detection:** the probe records per region and standard whether native is on and readable, which
  feeds the Coverage view and Automatic mode.

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
- **Size:** about 120 checks to start, covering the CIS and FSBP/MCSB controls that matter most.
- **Why CEL and not Rego:** it is small, side-effect free and fast enough to embed in the probe, and
  the people who write the checks already know it from Kubernetes.

### Findings

- **`posture_findings`:** one row per source, control and resource key. It holds:
  - severity and status;
  - first seen, last seen, resolved at;
  - suppression: reason, until, by;
  - evidence (small);
  - the standards and controls.
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
  - Compute Optimizer;
  - Azure Advisor;
  - Obzly checks;
  - **Obzly agent metrics:** CPU and memory p95 over 14 days. CloudWatch alone has no memory data.
    Resources are joined through inventory: EC2 instance ID ↔ `host_id`.
- **Prices:** savings use the resource's own effective cost from cost data, scaled by list-price
  ratios from the AWS Price List and Azure Retail Prices APIs.
- **Storage:**
  - `cost_recommendations` is replaced on each refresh;
  - `recommendation_states` keeps open, planned (owner, due date), done and dismissed (reason) by a
    stable key, so state survives refreshes.
- **Done is detected** when inventory and cost data show the change.
- **Realized savings:** the median daily cost 14 days before vs 14 days after, shown against what
  was expected.

### Commitments

- **Existing commitments:** Savings Plans, EC2 and RDS Reserved Instances, and Azure reservations
  and savings plans, with coverage, how much each is used, and when it ends.
  - A commitment used 0% (for example an RDS reservation for a family that was migrated) is flagged.
  - So is one ending within 60 days; that also alerts.
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
    data, so it appears only for connections with an hourly export. Otherwise the provider's
    recommended commitment is used.
  - **How to buy** lists the console steps. Obzly never buys.

### Posture

- **Actions:** each groups the findings that one change fixes. Examples:
  - "Lock down contoso-logs-eu: require TLS and turn on access logging" closes two findings;
  - "Delete 6 unattached, unencrypted EBS volumes" is linked to the matching cost recommendation.
- **Guardrails** stop a finding coming back:
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
| Cost: API, forecasts and recommendations | `ce:GetCostAndUsage`, `GetCostForecast`, `GetDimensionValues`, `GetTags`, `GetSavingsPlansPurchaseRecommendation`, `GetReservationPurchaseRecommendation`, `GetSavingsPlansCoverage` / `Utilization`, `GetReservationCoverage` / `Utilization`; `savingsplans:DescribeSavingsPlans`, `ec2:DescribeReservedInstances`, `rds:DescribeReservedDBInstances`, `compute-optimizer:Get*Recommendations`, `GetEnrollmentStatus`, `pricing:GetProducts` | `Cost Management Reader` (and `Billing Reader` at billing scope), `Reservations Reader`; Advisor through `Reader` |
| Posture | `SecurityAudit` (baseline), `securityhub:GetFindings`, `DescribeHub`, `GetEnabledStandards`, `ListSecurityControlDefinitions` | `Security Reader` |

This replaces the cost and posture rows of the table in `INFRASTRUCTURE.md`.

## APIs

New Site routes are served through the control plane's `/sites/{id}` proxy and added to its
allowlist (see the `site-scoped-api` checklist). Admins and project members can read; changing a
finding, recommendation or budget needs a project member.

- **Cost:**
  - `/api/v1/cloud-cost/summary`, `/explore` (range, period, group, filters, metric, currency),
    `/facets`, `/status`;
  - `/budgets`, `/anomalies`;
  - `/recommendations` (and `PATCH` for status), `/commitments`, `/planner`.
- **Posture:**
  - `/api/v1/posture/standards`, `/findings`, `PATCH /findings/{id}` (suppress or unsuppress);
  - `/trends`, `/coverage`;
  - `/actions` (and `PATCH` for plan or dismiss).
- **Control plane:** `/api/v1/saved-views` (CRUD; a shared view is read-only to everyone but its
  owner), plus each user's default view per tab.
- **MCP:** read-only tools: cost summary, explore, anomalies, budgets, recommendations, posture
  scores, findings, coverage. Each is scoped to the caller's projects, and resource names and tags
  are marked as data, not instructions.

## Security and privacy

- **Credentials:** the same as inventory. The probe's own identity, a role with an external ID, or
  federation through the control plane. Obzly stores only identifiers.
- **Raw billing line items** stay in the customer's bucket. Only daily aggregates and tracked tags
  are sent.
- **Baseline checks** run in the probe; only findings and minimal evidence leave it.
- **Audit:** suppressions, dismissals, plan changes and shared views are written to the audit log.

## Build phases

1. **Cost (v1.1.0):**
   - the cost use with source choice and billing scope;
   - FOCUS 1.2 export readers (AWS, Azure) and the API fallback;
   - Site tables;
   - Overview and Explorer with saved views;
   - budgets (managed rules) and anomalies;
   - the asset drawer's cost section.
2. **Posture (v1.2.0):**
   - native ingestion (Security Hub, Defender for Cloud);
   - the findings lifecycle and scope runs;
   - Standards, Findings, Trends and Coverage;
   - the first baseline checks;
   - alerts for new critical and high findings.
3. **Recommendations (v1.3.0):**
   - cost recommendations with states and realized savings;
   - commitments and the planner;
   - posture actions, guardrails and the score simulator;
   - MCP tools.

Each phase updates the role and Bicep templates for its permissions, and the docs and upgrade
runbook.

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
