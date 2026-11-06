# Synthetic monitoring (design)

Status: **locked** (signed off 2026-10-25, together with the UI mock). It ships in v0.55.0 with the
pending fixes, Infrastructure inventory and shared notification channels (a FEATURE release).

## Goal

Test APIs and endpoints from several locations on a schedule. A test covers:

- reachability;
- response time with a phase breakdown;
- assertions on the response;
- certificate validity.

Results are shown per check, both aggregated across locations and run by run. This is a
**control-plane feature** that puts no meaningful load on the control plane or on Sites.

## Shape

```
              config (pull, ETag, every 180 s)            results (push, OTLP, batched)
  obzly-probe  ─────────────────────────────▶  Control plane     obzly-probe ────────────▶  Site
  (location X)  ◀─── checks + destinations ───  (definitions,                          "Synthetics" project
                                                 probes, UI)  ◀── PromQL / logs via proxy ── (metrics, logs,
                                                                                              alert rules)
```

- **Control plane:** holds check definitions, probes and their tokens, and results destinations.
  It serves each probe's config and shows the views. It never schedules runs or stores results.
- **Probe** (`obzly-probe`, a separate small binary):
  - pulls its config;
  - runs the checks on its own schedule;
  - pushes results as OTLP straight to the destination Site. The control plane's proxy doesn't
    carry OTLP ingest, and it shouldn't.
- **Site:** stores results in an ordinary project. Dashboards, PromQL, retention and **alert
  rules** (webhook, Slack, PagerDuty, email) work unchanged.

Load: about 1 config poll per probe every 180 s, almost always answered `304`, plus small OTLP
batches to the Site. Checks default to a 180 s interval too, so a probe running many monitors
stays light. A check can go as low as 30 s when you choose.

## Concepts

| Concept | What it is |
| --- | --- |
| **Probe** | One running `obzly-probe`. It has a name, a **location** label (e.g. `aws-ap-south-1`, `office-blr`), an optional region/provider, a token, and `last_seen_at`/version reported on each poll. |
| **Probe token** | `obz_prb_…`, shown once. Stored as a SHA-256 hash (`access_tokens.HashToken`), with prefix, scopes, expiry and `revoked_at`, following `access_tokens`. It is bound to the probe, not to a user. |
| **Destination** | Where results go: a Site, a project on it, that project's ingest API key (`lb_…`, encrypted with `SecretKeyRepository.Encrypt`) and the Site's OTLP URL as the probes see it. Both ways of getting the key are supported: **create it through the proxy** (`/projects/api-keys` is already allowed) or **paste** an existing one. |
| **Check** (monitor) | Type, target, interval (default 180 s, minimum 30 s), timeout, assertions, locations (all, or a list), destination, and **TLS verification on/off**. Check secrets (auth headers, basic auth) are encrypted at rest, sent only to probes, and never returned to the UI. |

## Check types (Phase 1)

- **HTTP/API:**
  - request: method, URL, headers, body, follow redirects (on/off), and `tls.verify` (on by default);
    turning it off accepts self-signed, expired or untrusted certificates, for internal services;
  - assertions: status (equals/in range), response time below N ms, header contains, body
    contains or regex, and JSON path equals/exists (`$.status == "ok"`).
- **TLS certificate:** days to expiry, chain valid, hostname matches, minimum protocol (TLS 1.2).
  It runs with any HTTPS check or on its own against `host:port`.
  - With `tls.verify: false` the connection still succeeds on an invalid certificate. Expiry, issuer
    and subject are still reported, and a `tls_verified=false` label on `synthetics_tls_info` and
    `synthetics_cert_expiry_seconds` (and on the run's log line) marks it, so dashboards show which
    checks skip verification.
  - A TLS check can also assert "must be self-signed by this issuer" for internal CAs.
- **TCP:** the port accepts a connection within the timeout.
- **DNS:** the name resolves, optionally to an expected set or a record type (A/AAAA/CNAME/MX/TXT),
  optionally against a given resolver.
- **ICMP:** packet loss and round-trip time. It needs `CAP_NET_RAW` or unprivileged ping sockets
  (`net.ipv4.ping_group_range`); the probe reports "unsupported" rather than failing silently.

Later phases:

- multi-step API checks (variables carried between steps);
- maintenance windows;
- browser page loads (headless Chromium, Web Vitals, screenshot on failure) as a separate,
  heavier probe image.

## Results format (OTLP)

Metrics, all with the labels `check_id`, `check`, `type`, `location`, `probe`, `target`. Every
run of a monitor carries the same labels, so each metric is one series per monitor and location.
Labels that only some runs have (`tls_verified`) are on the TLS metrics alone, and the metrics
resource carries `service.name` and `host.name` but not `service.version`, so a probe upgrade
doesn't start new series (the log lines keep it).

| Metric | Meaning |
| --- | --- |
| `synthetics_up` | 1 if the run passed (all assertions), else 0 |
| `synthetics_duration_seconds{phase=dns\|connect\|tls\|ttfb\|transfer\|total}` | Timing breakdown of passing and warning runs (a failed run's time, often the timeout, is only in its log line). Phases are summed over redirect hops, so they add up to about the total |
| `synthetics_http_status` | Last status code |
| `synthetics_cert_expiry_seconds{tls_verified}` | Time from the run's start until the certificate expires |
| `synthetics_icmp_rtt_seconds`, `synthetics_icmp_jitter_seconds` | ICMP round trip and jitter, for runs that got at least one reply |
| `synthetics_icmp_loss_ratio` | ICMP packet loss, every run |
| `synthetics_response_bytes`, `synthetics_redirects` | HTTP response size and redirects followed (0 when following is off) |
| `synthetics_dns_answers` | Number of records returned (DNS checks) |
| `synthetics_warn` | 1 if the run passed but breached a warning threshold (latency, certificate, `warn` assertion) |
| `synthetics_tls_info{tls_verified,issuer,protocol,cipher}` | Always 1; carries TLS details as labels (sent with every run, so it is never stale in an instant query) |
| `synthetics_step_up{step,step_index}`, `synthetics_step_duration_seconds{step,step_index}` | Multi-step: each step that ran; durations only for steps that passed |

There is also **one log line per run** (`service.name=obzly-probe`): level `info` on success,
`warn` on a warning, `error` on failure. It records:

- every assertion and its result;
- status and headers;
- the timing waterfall;
- the first 1 KB of the response;
- the resolved IPs;
- the certificate subject, SANs, issuer, expiry, protocol and cipher;
- the error text.

That gives run history and run detail without new storage. Details that vary per run stay in the
log line rather than in metric labels, to keep metric cardinality low.

## Control plane

- **Tables** (CP-only by convention, like `sites` and `site_grants`: they exist everywhere and stay
  empty on Sites): `synthetic_probes`, `synthetic_probe_tokens`, `synthetic_destinations`,
  `synthetic_checks` (its locations are a JSON list on the row; empty means every location).
- **Probe API**, authenticated with the probe token, registered in `setUpControlPlaneRoutes`:
  - `GET /api/v1/synthetics/probe/config` with `If-None-Match`:
    - returns the probe's checks, with secrets decrypted, and destinations (OTLP URL + key). Each
      check also carries its `target` label as written (placeholders, not values) and the secret
      values filled in (`redact`), which the probe replaces with `[redacted]` in errors, body
      snippets, headers and assertion results, so no secret reaches a metric label or a log line;
    - the response ETag is a hash of the config, and it answers `304` when unchanged. The built
      config is cached per group until a monitor, destination or cloud connection changes (or
      10 minutes pass), so polls don't decrypt every monitor's secrets again;
    - it updates `last_seen_at`/version at most once a minute;
    - probe tokens are resolved through a Valkey cache, like `api_keys`; unknown tokens are
      remembered for 30 s too;
    - request bodies on probe routes are limited to 1 MB (3 MB for a YAML import), and a scan's
      reported errors, regions and types are trimmed before they're stored.
  - `GET /api/v1/synthetics/probe/tasks?wait=25`: one idle long-poll per probe for interactive
    **Test** runs. It returns at once when a test is queued for that probe, else `204` after the
    wait (at most 55 s; probes default to 25 s so a proxy with a 30 s read timeout, like
    CloudFront's default, doesn't cut it). Idle connections cost the control plane almost nothing;
    a group may hold 64 open at once per control plane replica (more get `429`).
  - `POST /api/v1/synthetics/probe/tasks/{id}/result`: the probe posts the test result back. The
    control plane keeps it in Valkey for 10 minutes for the editor to read. This is the only result
    the control plane ever holds, and it's interactive only.
- **Admin API** (session + MFA as today):
  - CRUD for probes, tokens (create, revoke), destinations and checks;
  - **clone** a check (copies everything except its name; secrets are copied encrypted, never
    exposed);
  - **YAML import/export** (below);
  - **test** a definition (saved or not) from chosen locations, or from the control plane itself
    when no probe can reach the target. That run reaches **public addresses only**, as alert
    webhooks do: no loopback, private, CGNAT, link-local or cloud metadata addresses, also when
    carried in NAT64 or 6to4 IPv6 (`ALERT_EGRESS_ALLOWLIST` and `ALERT_EGRESS_DENYLIST` widen and
    narrow it), and a DNS check's resolver is held to the same rule. It is labelled as such. Test
    an internal service from a probe;
  - **read** monitors and cloud connections: admins get whole definitions; other signed-in users
    get a summary for the dashboards (type, target or URL, folder, locations, destination, status),
    without request headers, bodies, assertions or secret names;
  - **enable/disable**;
  - a **status summary** for the overview.
- **Data:** charts and lists read the destination Site through the existing proxy:
  - `POST /sites/{site}/api/v1/metrics/query/promql/{project}` for metrics;
  - `/logs/query/...` for run history and run detail.

## Health status

Used everywhere a monitor appears: the list, the location grid, the overview, and the tab badge.

| Bullet | State | When |
| --- | --- | --- |
| 🟢 Green | Up | Every location passed its latest run |
| 🟠 Amber | Degraded | Some locations failing but fewer than `down_locations`; or a run passed but breached a warning (latency above `warn_ms`, certificate expiring within `warn_days`, an assertion marked `severity: warn`); or a probe is overdue reporting |
| 🔴 Red | Down | Failing from at least `down_locations` (default: half the locations, at least 1) |
| 🔵 Blue (round bullet) | Disabled | Paused or deactivated; no runs scheduled |

Status is computed from the latest run per location, from `synthetics_up` and `synthetics_warn`, so
it needs no extra storage.

## UI (control plane, "Synthetics" tab)

- **Overview** (top of the tab):
  - status counts as a donut (green / amber / red / blue) and a "monitors up" trend over the range;
  - an **all-monitors availability timeline** (one row per monitor, worst first);
  - a **location health matrix** (location × monitor);
  - top 10 slowest (p95) and most failing;
  - certificates expiring soon;
  - active outages.
- **Monitors list:**
  - one row per monitor: status bullet, type, name, a location grid, uptime %, p95, certificate
    days left;
  - row actions are **icon buttons with a tooltip** on hover and focus, and an `aria-label`: Edit
    (pencil), Clone (copy), Disable (pause) / Enable (play), Delete; **Import YAML** /
    **Export YAML** in the toolbar;
  - **sort from the table headers**: Status, Type and Name show up/down caret icons; clicking a
    header sorts by it ascending, clicking again reverses (`aria-sort` set). Status order is red →
    amber → green → blue, so problems come first, with name as the tie-break;
  - **status filter chips** (All / Down / Degraded / Up / Disabled), plus type and location filters
    and a name search;
  - the sort and filters are remembered per user and reflected in the URL, so a view can be
    shared.
  - **Clone** creates a disabled copy ("… (copy)") without opening the editor.
- **Editor** — no tab or link of its own, to avoid accidental overwrites: it opens only from an
  **Edit** icon or **New monitor**.
  - A breadcrumb ("← Monitors / name / Edit") replaces the tabs while it's open, with the loaded
    version ("Version 7 · saved 2 h ago").
  - Any change shows an **Unsaved changes** badge; Cancel, Back or navigating away then asks
    in-page "Discard your unsaved changes?" (Keep editing / Discard).
  - Saves send the loaded version (`If-Match`); if someone else saved in between, the save is
    rejected with "changed by someone else, reload".
  - **Test** runs the definition, saved or not, from one location or all, and shows the result
    inline within seconds (through the task long-poll):
    - pass/fail per assertion;
    - status, headers and response snippet;
    - a timing waterfall (DNS → connect → TLS → TTFB → transfer);
    - certificate details and resolved IPs.
  - **Save** stores it and returns to the list. **Test & save** saves only if the test passes, with
    an option to save anyway.
- **Monitor detail** is a **fly-out**: a wide panel from the right over the list, opened by
  clicking a monitor row or an overview strip. Its header (status, name, type, target, and icon
  actions Run now / Edit / Clone / Disable, Close) stays fixed while the charts scroll; Esc closes
  it, and a run's detail opens on top of it. Over the chosen range with a location filter:
  - header: status, uptime %, SLO and error budget left, p50/p95/p99, last and next run,
    certificate days left;
  - **availability timeline:** one strip per location, coloured per run, with outage periods
    under it (start, duration, locations, first error);
  - **response time by location** (p50/p95 lines);
  - **phase breakdown:** stacked area of DNS / connect / TLS / TTFB / transfer, showing where time
    goes;
  - **availability heatmap** (location × hour);
  - **status codes and failure reasons:** assertion, timeout, DNS, connection refused, TLS;
  - **TLS panel:** issuer, subject/SANs, protocol, cipher, a days-to-expiry countdown with
    thresholds, and whether verification is on;
  - **response size and redirects** over time, and **resolved IPs** for DNS checks;
  - **ICMP:** round-trip time, jitter, loss;
  - **locations table:** uptime, p95, last result and last error per location;
  - **recent runs:** each row opens the run detail (waterfall, assertions, response snippet,
    error).
- **Probes:** location, last seen, version, monitors assigned, and install snippets (Linux/systemd,
  Docker, Helm) with the token.
- **Alerts:** shortcuts that create Site alert rules on the destination project through the proxy:
  - "down from N of M locations": `count by (check) (synthetics_up == 0) >= N`;
  - "p95 above X ms";
  - "certificate expires within D days".

## Bulk provisioning with YAML

- **Import:** upload a YAML file (or paste it) on the Checks page, or `POST
  /api/v1/synthetics/checks/import`.
  - Everything is validated first. A **preview** lists what will be created, updated and left
    unchanged, plus errors per line, before anything is applied. Nothing is half-applied: the
    whole file goes in or none of it does.
  - Checks are matched by `name`, so importing the same file again updates them instead of
    creating duplicates (idempotent). `prune: true` at the top deletes checks that aren't in the
    file, for GitOps-style management.
  - Secrets may be written inline or as `${env:NAME}` placeholders. The importer asks for the
    placeholder values once and stores them encrypted. Exported files always contain
    placeholders, never secret values.
  - A placeholder with no stored value is filled from the **probe's environment**, but only for
    names starting with `OBZLY_SECRET_` or listed in the probe's `OBZLY_PROBE_ENV_ALLOW`
    (comma-separated; a trailing `*` is a prefix). `OBZLY_PROBE_*`, `AWS_*`, `AZURE_*` and
    `GOOGLE_*` are never read, even when listed, so a monitor can't send the probe's token or
    cloud credentials anywhere. Any other name fails the run with an error naming the
    placeholder (never a value).
- **Export:** downloads the selected (or all) checks in the same format, for backup, review in
  git, or copying between control planes.
- **Sample file:** [`docs/examples/synthetics-checks.yaml`](examples/synthetics-checks.yaml), also
  offered as a download on the Import dialog. It covers every check type, assertions, locations,
  intervals, secrets and an insecure-TLS internal service.

## Probe (`obzly-probe`)

- **Code:**
  - `backend/cmd/probe` in the same Go module, built `CGO_ENABLED=0` (small, static);
  - checks run in `internal/synthetics/checks` (shared with the control plane's Test) and results
    go out as OTLP/HTTP through `internal/synthetics/report`;
  - outbound HTTP uses the hardened dialer/transport from `control_plane/services/egress.go`,
    extracted to a shared package. By default it blocks loopback, link-local (169.254/16) and
    the other cloud metadata addresses (`fd00:ec2::254`, `168.63.129.16`, `100.100.100.200`,
    `192.0.0.192`), also when carried in NAT64, 6to4 or IPv4-compatible IPv6, and allows private
    ranges (probes test internal services); `OBZLY_PROBE_DENY_CIDRS` adds more. A DNS check's
    resolver goes through the same rule;
  - results are sent to the Site without following redirects, so the ingest key goes nowhere else.
- **Config:**
  - environment: `OBZLY_CONTROL_PLANE_URL`, `OBZLY_PROBE_TOKEN`, and optional
    `OBZLY_PROBE_MAX_CONCURRENCY` (default 256), `OBZLY_PROBE_TASK_WAIT`, `OBZLY_PROBE_DENY_CIDRS`,
    `OBZLY_PROBE_ENV_ALLOW`, `OBZLY_PROBE_HEALTH_ADDR`, `OBZLY_PROBE_DATA_DIR`
    ([configuration](configuration.md#synthetics-probe-obzly-probe)).
    The location is the probe's, set on the control plane;
  - the last good config is cached on disk, so a probe keeps running checks if the control plane
    is briefly unreachable.
- **Safety:**
  - minimum check interval 30 s, timeout ≤ interval;
  - a per-probe concurrency cap, with jittered start times so a probe's checks don't all fire
    together;
  - response bodies are read up to 1 MB and only 1 KB is reported.
- **Packaging:**
  - binaries `obzly-probe_linux_{amd64,arm64}` + `checksums.txt` + `install.sh` on each GitHub
    Release (named without the version, so `releases/latest/download/...` always works);
  - a systemd unit + install script;
  - image `ghcr.io/obzly/obzly-probe:<version>` (distroless);
  - Helm chart `deploy/helm/obzly-probe` (Deployment + Secret).

## Release pipeline

In `.github/workflows/ci-release.yml`:

- **image:** a second `build-push-action` from `Dockerfile.probe` to `ghcr.io/obzly/obzly-probe`
  with the same tags (the package is private on first push, like the main image);
- **release:** the release job builds the static binaries and attaches them with `install.sh`, the
  systemd unit and `checksums.txt`;
- **chart:** `helm.yml` lints the new chart.

## Phase 1 scope

- **In:**
  - the probe, its token and pull config;
  - HTTP/API, TLS (including `tls.verify: false`), TCP, DNS and ICMP checks;
  - clone, YAML import (preview, idempotent, optional prune) and export, with the sample file;
  - the overview, monitors list (status bullets, sorting, filters), editor with Test, and monitor
    detail charts;
  - destinations;
  - OTLP metrics + run logs;
  - the Checks, Check detail and Probes pages;
  - the alert shortcuts;
  - packaging (binary, image, chart, systemd).
- **Out:** browser checks, multi-step checks, maintenance windows, a public status page, and
  control-plane relay of results for probes that can't reach a Site.

## Decisions (2026-10-25)

1. Destination keys: both, create through the proxy or paste an existing one.
2. Defaults: config poll 180 s and default check interval 180 s (minimum 30 s), so a probe with many
   Sites or monitors stays light.
3. Private targets: allowed by default; cloud metadata (169.254/16) and loopback stay blocked.
4. Also in Phase 1: clone a check, YAML import/export with a sample file, and `tls.verify: false`
   for self-signed or otherwise invalid certificates.
5. Also in Phase 1: Test / Save / Test & save in the editor; green / amber / red / blue status;
   monitor detail charts covering everything a probe measures; a global overview; sorting by
   status, type and name.

## v0.56.0 additions

Requested 2026-10-25 and built in v0.56.0. Notes on where the build differs from the request are
marked **Built:**.

### Folders

- A monitor sits in one **folder**, a path such as `Payments/EU` (`synthetic_checks.folder`, `''`
  for none). Folders exist through their monitors; there is nothing to create or delete separately.
- **Monitors table:**
  - rows are grouped under collapsible folder headings, each with its own status counts;
  - a folder picker filters;
  - sorting applies within each folder.
- **Editor:** a folder field with completion from existing folders.
- **YAML:** a monitor's `folder:` is imported and exported.

### Dependencies (parent / child)

- A monitor can name **parents**: other monitors it depends on, picked from a list of the existing
  ones (`synthetic_checks.parent_ids`). A child can have several parents, and the editor refuses a
  cycle.
- **While a parent is Down, its children are Impacted:**
  - a new status, amber with a half-filled bullet;
  - the reason says "Parent api-gateway is down";
  - children keep running, so their history stays complete;
  - the status, map and overview counts all show Impacted.
- **Alerts are muted for impacted children,** so one outage sends one alert, for its root cause:
  - **How:** each child's alert rule ends with `unless` its parents are down.
  - **Limit:** this works when parent and child send to the same destination project, which is the
    usual case.
  - **Different projects:** a parent in another project still makes the child Impacted in the UI,
    and the editor warns that alerts can't be muted across projects.
- **Dependency map:** a graph view of monitors with their status colours, showing parents above
  children, and opening the detail on click. **Built:** a layered map (each monitor one row below
  its deepest parent), drag to move and scroll to zoom; monitors without dependencies are left out
  and counted. Path highlighting is not built.
- **Built:** a failing (down or degraded) monitor with a down parent is Impacted, judged on each
  parent's own results, so a chain marks every level below the first down monitor. The reason is
  a tooltip ("api-gateway down: alerts muted until it recovers") and an alert on the detail.
- **Monitor detail:** a Dependencies panel listing its parents and children.

### Alerts per monitor

- **Editor → Alerts:**
  - notification channels, picked from Obzly settings' channels;
  - when to notify: Down (default), also Degraded, certificate expiring within N days;
  - a repeat interval.
- **Delivery:**
  - The control plane adds each monitor's alert settings to the shared-config document every Site
    already pulls every 5 minutes.
  - The monitor's destination Site then creates and maintains a **managed alert rule** in the
    destination project (`source = synthetics`, read-only in Alerts, removed with the monitor).
  - Channels have the same IDs on the control plane and the Sites, so no new credentials or
    control-plane-to-Site calls are needed. YAML imports get alerts too.
- **Rule:** fires when at least `down_locations` locations fail
  (`count(min by (location) (last_over_time(synthetics_up{check_id="…"}[…])) == 0) >= N`), `unless`
  a parent is down (above). Grouping by location counts a location once even when its runs are in
  several series (probes before v0.58.1 labelled runs that reached TLS separately).
- **Built:**
  - Rules are keyed `synthetics:<monitor>:down|degraded|cert` in `alert_rules.managed_by`; the
    Alerts tab shows them with a "Synthetics" tag and refuses edits (409).
  - The window is two intervals plus a minute; the rule runs every interval, between 60 s and 5
    minutes. The degraded rule is muted by a down parent too, and by its own down rule.
  - Channels are the central ones (Obzly settings on the control plane); a channel scoped away from
    the destination project is still sent to it for this rule.
- **Timing:** new or changed alerts reach the Site within 5 minutes; the editor says so.

### Probe groups

- A **probe group** is a location with any number of probe instances. It has one enrollment token,
  and instances join it by name: the pod name in Kubernetes, the host name on a VM.
- Existing probes become groups of one, keeping their tokens and locations.
- **Scheduling: spread with failover.**
  - Each monitor runs on **one** live member of each group per interval, chosen by rendezvous
    hashing of the monitor ID over the live members. Data volume is the same as one probe.
  - Members heartbeat through the long-poll they already keep open (every 25 s), and a member that
    misses 60 s leaves. A member that shuts down cleanly leaves at once (`POST
    /api/v1/synthetics/probe/leave`).
  - Its monitors move to the others on their next heartbeat, so typically within a minute.
  - Every member uses the same schedule: a monitor runs at a fixed offset (from a hash of its ID)
    within each interval of wall-clock time, so a monitor moving between members, or a member
    restarting, neither runs it twice nor skips a run.
  - Test runs go to any live member.
- **Probes tab → Probe groups:**
  - each group: location, members (name, version, last heartbeat, monitors it currently runs),
    health (all members up, some missing, none);
  - **Built:** members heard from in the last hour are listed (an instance silent for an hour, such
    as a reclaimed spot node, is forgotten); the group is amber only when an
    instance stopped in the last 10 minutes (a Kubernetes rollout renames pods, so older stops are
    expected), red with no live instance. An instance names itself with `OBZLY_PROBE_INSTANCE`
    (default: the host name; the chart sets the pod name); probes older than v0.56.0 count as one
    instance named `default`;
  - **Add group** shows the install snippets with the group token;
  - **Rotate token** issues a new token, and members on the old one keep working for 24 h;
    tick **Revoke the old token now** (`?revokeOld=true`) when it leaked, and it stops at once;
  - a group takes at most 64 instances heard from in the last day (a new name beyond that gets
    `429`), and instances not heard from for a day are forgotten.
- **Helm chart** (`deploy/helm/obzly-probe`):
  - **`replicas: 3`** by default, adjustable;
  - all replicas share the group token;
  - preferred anti-affinity spreads them across nodes and zones;
  - a PodDisruptionBudget keeps at least `replicas - 1` running;
  - **Built:** a service account (with `serviceAccount.annotations` and `podLabels`) for the probe
    group's own cloud identity when it scans cloud connections (docs/INFRASTRUCTURE.md).
  - `install.sh` and the systemd unit take the same group token, and each VM joins as a member.

### Smaller changes

- **Monitors table:** the icon-action column gets an **Actions** header.
- **Sidebar:** Infrastructure and Synthetics move up, next to Dashboards.
- **Availability strips:** a slot shorter than a monitor's interval (the 1 h view's 75 s slots and a
  3-minute monitor) shows the latest run's state until the next run is due, instead of grey "no
  runs" between runs; a missed run still shows grey.
- **Tooltips** on strips and status dots open above them, clear of the pointer.

## v0.57.0 additions (design, pending sign-off)

Two items from "Later phases": multi-step API checks and maintenance windows. Browser checks stay
later.

### Multi-step API checks

A monitor type **Multi-step API** (`type: multi`) runs up to **10 HTTP steps in order**, on one
probe instance, as one run. Typical use: sign in, call an API with the token, check the result,
clean up.

- **A step** has:
  - a name;
  - a request (method, URL, headers, body, follow redirects), with the monitor's `tls` options;
  - assertions, the same kinds as an HTTP monitor (status, response time, header, body, JSON
    path), each `fail` or `warn`;
  - **extract**: values to carry forward, each `{name, from, path, secret}`. `from` is `json`
    (a JSON path), `header`, `status`, `regex` (first capture group of the body) or `body`.
- **Variables:** `{{name}}` is replaced in later steps' URL, headers, body and assertion values.
  - Monitor-level `variables` set starting values. These can be `${env:OBZLY_SECRET_…}` (resolved
    on the probe, with the same allowlist as HTTP monitors) or stored secrets.
  - Built-ins: `{{$uuid}}`, `{{$timestamp}}` (Unix seconds), `{{$isoTimestamp}}`,
    `{{$randomInt}}`.
  - An extracted value marked `secret: true` is never written to the run log or shown in Test
    results, and neither are `${env:}` or stored-secret values.
  - Cookies set by a step are sent by the later steps of the same run (a cookie jar per run, on by
    default, `cookies: false` turns it off).
- **Failure:** the run stops at the first step with a failed `fail` assertion, and later steps
  show as **skipped**. `continue_on_failure: true` on a step keeps going (for clean-up steps). A
  `warn` failure never stops the run.
- **Timeouts:** each step has its own (default 10 s), and the monitor's total timeout (default 60 s,
  at most 120 s) bounds the run.
- **Limits:**
  - up to 1 MB of each response is read for assertions and extraction;
  - an extracted value is at most 4 KB;
  - a run makes at most 10 requests, redirects not counted.
- **Results:** `synthetics_up` and `synthetics_duration_seconds{phase="total"}` describe the whole
  run, so status, uptime, the overview and alerts work unchanged.
  - New metrics: `synthetics_step_up{step, step_index}` and
    `synthetics_step_duration_seconds{step, step_index}`.
  - The run log line lists every step: request line, status, timing waterfall, assertions, and
    extracted variable names (values only when not secret), or "skipped".
- **Editor:**
  - a step list (add, duplicate, reorder by drag or arrows, delete);
  - each step collapses to one line ("2. GET /orders/{{orderId}} · 3 assertions · extracts
    status");
  - a Variables panel lists what each step extracts and where it's used, and flags a
    `{{name}}` that no earlier step or variable defines.
  - **Test** runs the whole sequence from one probe and shows each step's result, waterfall and
    extracted values (secrets masked).
- **Monitor detail:** adds a **Steps** panel with success rate and p95 per step, as a stacked
  duration chart, so a slow or failing step stands out. Recent runs show which step failed.
- **YAML:**

  ```yaml
  - name: Checkout API flow
    type: multi
    interval: 5m
    variables:
      user: synthetic@example.com
      password: ${env:OBZLY_SECRET_CHECKOUT_PASSWORD}
    steps:
      - name: Sign in
        request:
          method: POST
          url: https://api.example.com/auth/login
          headers: { Content-Type: application/json }
          body: '{"email":"{{user}}","password":"{{password}}"}'
        assertions:
          - status: 200
        extract:
          - { name: token, from: json, path: $.accessToken, secret: true }
      - name: Create order
        request:
          method: POST
          url: https://api.example.com/orders
          headers: { Authorization: "Bearer {{token}}" }
          body: '{"sku":"TEST-1","qty":1}'
        assertions:
          - status: 201
          - json: { path: $.state, equals: pending }
        extract:
          - { name: orderId, from: json, path: $.id }
      - name: Cancel order
        continue_on_failure: true
        request:
          method: DELETE
          url: https://api.example.com/orders/{{orderId}}
          headers: { Authorization: "Bearer {{token}}" }
        assertions:
          - status: 204
  ```

- **Built (v0.57.0):**
  - A step whose `{{name}}` was never set (an earlier extraction failed or was skipped) isn't
    sent; it fails with "`{{name}}` has no value", so a clean-up step never calls e.g.
    `DELETE /orders/`.
  - A failed step extracts nothing, and a failed extraction (path not found, value over 4 KB) fails
    its step.
  - Response bodies are kept only for steps that didn't pass.
  - A YAML file's `defaults.timeout` applies to multi-step monitors too.
  - Multi-step runs report no run-level HTTP status, phase or TLS metrics; the per-step metrics
    replace them.
  - Probes older than v0.57.0 report "unknown check type multi": upgrade probes first.

### Maintenance windows

A window says when monitors are expected to be down, so they don't page anyone and don't count
against uptime.

- **Where:** Synthetics → **Maintenance** tab on the control plane. Admins and Synthetics editors
  manage windows.
- **A window has:**
  - a name and a reason;
  - a scope: chosen monitors, folders (including subfolders), or all monitors;
  - a schedule:
    - **one-off:** start and end;
    - **recurring:** daily, weekly on chosen days, or monthly on day N or the Nth weekday, from a
      start time for a duration, in a chosen time zone, with an optional end date;
  - enabled, or paused.
- **During a window** runs continue (the data stays complete), and:
  - **Status:** a monitor shows **Maintenance** (a wrench icon, slate) instead of red or amber.
    Green stays green, so you can see a change finish early.
  - **Alerts are muted:**
    - The control plane sends each window's occurrences for the next 48 hours to the monitor's
      destination Site, with the shared config the Site already pulls every 5 minutes.
    - The Site keeps them as **managed silences** matching the monitor's `check_id` label on its
      managed rules. They show as read-only in the Site's Alerts → Silences, tagged
      "Synthetics maintenance".
    - An alert already firing when the window starts doesn't re-notify during it.
    - A monitor still down when the window ends notifies at the next evaluation.
  - **Uptime and SLO** leave the window out. Availability strips show it hatched, and the error
    budget doesn't spend it.
  - **Dependencies:** children of a monitor in maintenance still show Impacted, and their alerts
    stay muted by the parent rule, as today.
- **Lists:**
  - The Maintenance tab shows active, upcoming (next occurrence) and past windows, with a
    calendar strip of the coming 14 days.
  - The monitors table and the overview show a wrench next to monitors in or entering
    maintenance within the hour.
  - Starting a window early or ending it now is one click, and recorded in the audit log.
- **YAML:** a top-level `maintenance:` list (name, reason, monitors or folders, schedule), imported
  and exported with the monitors.
- **API:**
  - `GET/POST/PUT/DELETE /api/v1/synthetics/maintenance`;
  - `POST /api/v1/synthetics/maintenance/{id}/end` ends the current occurrence now;
  - `GET /api/v1/synthetics/maintenance/occurrences?from&to` returns the expanded occurrences for
    charts and strips.
- **Built (v0.57.0):**
  - `POST /api/v1/synthetics/maintenance/preview` validates a schedule and returns its next three
    occurrences for the editor, so time-zone rules live in one place (Go).
  - **Start now** begins the next occurrence immediately for its planned duration (not until the
    planned end), so starting a weekly window early can't mute alerts for days.
  - **YAML** schedule keys: `repeat` (once, daily, weekly, monthly_day, monthly_weekday), `days`,
    `day`, `week`/`weekday` (1st to 4th or last), `at`, `duration`, `time_zone`, `from`, `until`,
    and `start`/`end` for one-off windows. `prune` deletes windows only when the file has a
    `maintenance:` key, so an older monitors-only file never removes windows.
  - **DST:** a start time skipped by a spring-forward change starts at the same instant (02:30
    becomes 03:30); a repeated time uses its first instance; durations are real time. A monthly day
    past the month's end uses its last day.
  - **Silences on the Site:** one per occurrence and project, matching `check_id` (a regex of ids
    when it covers several monitors), for enabled monitors with alert channels. Any silence, managed
    or not, now holds a due notification and sends it at the first evaluation after the silence
    ends if the alert still fires.
  - Monitor SLOs and error budgets don't exist yet, so the detail shows uptime without
    maintenance only; the error-budget stat in the mock is for later.
