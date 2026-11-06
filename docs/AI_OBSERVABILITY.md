# AI observability (design)

Status: **v0.57.0 additions pending sign-off.** Phase 1 (Claude Code usage from its OTel events,
the `ai_logs` stream and the AI Observability tab) shipped earlier; see ARCHITECTURE.md
["AI observability"](../ARCHITECTURE.md#ai-observability-claude-code-usage).

This release adds four things:

1. **Delta temporality** in the OTLP metrics receiver, so Claude Code's own metrics (and any
   delta-exporting SDK) work without extra settings.
2. A **Claude Code usage** dashboard template.
3. **Budgets**: spending limits per project, user or model, with alerts.
4. **LLM apps**: any application instrumented with the OpenTelemetry `gen_ai.*` semantic
   conventions, not only Claude Code.

## 1. Delta temporality

Today the converter treats every Sum and Histogram point as cumulative. A delta point (Claude
Code's default, and the default of the .NET and some Python and JS SDK setups) is stored as if it
were a running total, so `rate()` and `increase()` return nonsense. The onboarding snippet works
around it with `OTEL_EXPORTER_OTLP_METRICS_TEMPORALITY_PREFERENCE=cumulative`.

- **At ingest, delta becomes cumulative:**
  - for Sum points (monotonic or not) and Histogram points (`_sum`, `_count`, every `_bucket`)
    whose temporality is `DELTA`;
  - the receiver adds each point to a running total per series and stores the total, so the stored
    data and every query, dashboard and alert work the same as for cumulative senders.
- **Running totals live in Valkey**, so every api replica sees the same total:
  - one `INCRBYFLOAT` per point and one `EXPIRE` per series, pipelined so a request is one round
    trip;
  - keyed by project and series fingerprint;
  - forgotten 24 hours after a series' last point;
  - if the samples then fail to store (a full or unavailable ingest buffer), the increments are
    taken back so the sender's retry doesn't count them twice.
- **Resets are safe:**
  - A Valkey restart or an expired total starts again from zero, which PromQL treats as a counter
    reset: `rate()` and `increase()` stay correct, and only a raw "total" panel drops.
  - Points are added in arrival order; a late point adds to the current total rather than being
    inserted in the past, which is the OTel Collector's `deltatocumulative` behaviour too.
- **When Valkey is unreachable**, a request carrying delta points is answered 503 (gRPC
  `UNAVAILABLE`) so the sender retries, rather than storing wrong numbers. Cumulative-only requests
  never touch Valkey.
- NaN or infinite delta points are rejected as a partial success.
- `obzly_otlp_delta_samples_total{result}` counts converted and refused delta samples.
- **Not covered:** exponential histograms and summaries are still skipped, as today.
- The onboarding snippet keeps the cumulative setting for older Obzly Sites, but no longer needs
  it.

## 2. Claude Code usage dashboard

**Dashboards → New → From template → Claude Code usage**, an ordinary editable dashboard built
from Claude Code's metrics (which work with delta temporality, above).

- **Variables:** user (`user_email`), model, terminal type, app version.
- **Panels:**
  - cost today, this week, this month (stat);
  - cost over time by user and by model;
  - tokens by type (input, output, cache read, cache creation) and cache hit ratio;
  - sessions per day;
  - active time (user vs CLI);
  - lines of code added and removed;
  - commits and pull requests created;
  - edit-tool accept/reject rate by tool and language;
  - top 10 users by cost.
- **Requires:** the metrics exporter, which the onboarding snippet already turns on. With logs
  only, the AI Observability tab still works, and the template's panels show "no data" with a
  hint.

## 3. Budgets

**AI Observability → Budgets** (project owners and admins manage; members view).

- **A budget has:**
  - a name and an amount in USD;
  - a period: day, week or month, calendar-aligned in a chosen time zone (default UTC);
  - a scope:
    - the whole project;
    - each user (every user gets the limit);
    - each model;
    - one named user;
  - thresholds, default 80% (warning) and 100% (critical), editable;
  - notification channels (the shared channels from Obzly settings);
  - which spending counts: Claude Code, LLM apps, or both.
- **Spend comes from the same data as the AI Observability tab:** the `cost_usd` of Claude Code
  `api_request` events, plus LLM app cost where model prices are set (below). The tab and the
  budget can't disagree.
- **Evaluation:**
  - Every 5 minutes, by the Site's alert evaluator, as a **managed alert rule** per budget
    (`source = ai_cost`, `managed_by = ai-budget:<id>`), shown read-only in Alerts with a
    "Budget" tag.
  - The alert's labels carry the user or model.
  - Each threshold notifies **once per period**, per user or model, and resolves when the period
    rolls over.
- **List:** each budget shows the current period's spend against the amount as a progress bar
  (green, amber past the first threshold, red past 100%), the forecast for the period at the
  current rate, and, for per-user and per-model budgets, the top five.
- **Not a hard cap.** Obzly can't stop Claude Code or an app from spending; a budget only alerts.
  The doc and the editor say so.
- **API:**
  - `GET/POST/PUT/DELETE /api/v1/projects/{id}/ai-budgets`;
  - `GET /api/v1/projects/{id}/ai-budgets/status` returns spend, forecast and top five per budget.

## 4. LLM apps (OpenTelemetry `gen_ai.*`)

Applications instrumented with the GenAI semantic conventions show up in the AI Observability tab
next to Claude Code. This covers OpenLLMetry, OpenLIT, the official OTel instrumentations for
OpenAI, Anthropic, Bedrock and Vertex AI, LangChain, LlamaIndex, and others.

- **Source: spans**, already stored with their attributes. A span is an LLM call when it has
  `gen_ai.operation.name` (`chat`, `text_completion`, `embeddings`, `execute_tool`,
  `invoke_agent`, ...). The view reads:
  - provider: `gen_ai.provider.name`, or the older `gen_ai.system`;
  - `gen_ai.request.model` and `gen_ai.response.model`;
  - `gen_ai.usage.input_tokens` and `gen_ai.usage.output_tokens` (older `prompt_tokens` /
    `completion_tokens` too);
  - `gen_ai.response.finish_reasons`, `error.type` and the span status;
  - `server.address`;
  - the service name, duration and trace id.
- **New AI Observability tab: LLM apps.** Requests, tokens, errors, latency p50/p95 and estimated
  cost, by service, model, provider and operation over the chosen range.
  - The slowest and failed calls are listed with a link to their trace.
  - Agent and tool spans (`invoke_agent`, `execute_tool`) appear as steps inside a trace.
- **Metrics:** `gen_ai.client.token.usage` and `gen_ai.client.operation.duration` (histograms,
  often delta) are stored like any metric, so dashboards and alerts can use them.
- **Prompts and completions:** content events and attributes (`gen_ai.input.messages`, ...) are
  stored as sent but **not shown** in the LLM apps view; it never displays message content. Most
  instrumentations leave content capture off by default, and the onboarding snippet keeps it off.
- **Cost:**
  - Spans rarely carry a price, so **AI Observability → Settings → Model prices** sets USD per
    million input, output and cached tokens per model name (exact or prefix, e.g.
    `claude-sonnet-*`).
  - The table is empty by default, with a "fill in common models" button that adds a dated
    starting list to review. Prices change, and Obzly doesn't fetch them.
  - Cost shows only for models with a price; others show tokens with "no price set".
- **Onboarding:** Data Sources → **LLM app (OpenTelemetry)** with SDK snippets (Python, Node.js)
  pointing the app's OTLP exporter at the project.
- **API:** `/api/v1/ai-usage/llm/{overview,calls}` alongside the existing
  `/api/v1/ai-usage/{overview,sessions}`, both over the spans store with fixed, parameterised
  queries.

## Built in v0.57.0 (differences from the design above)

- **Budget scopes:** `project`, `each_user`, `each_model`, `user` (one named user) and `model` (one
  named model). There is no per-service scope. Weeks start on Monday.
- **Budget alerts:** one managed rule per budget (`managed_by = ai-budget:<id>`, every 300 s). Each
  alert series is one crossed threshold, labelled with the user or model, the threshold, the
  severity and the period's start date, so it notifies once per period and resolves at rollover. The
  editor shows two thresholds; the API accepts up to five.
- **Rule upkeep:** a leader-locked job re-creates missing budget rules at startup and every 5
  minutes. A Site replica older than v0.57.0 deletes budget rules when it applies the control
  plane's shared config, so budget alerts can be missing for a few minutes during a mixed-version
  rollout.
- **Budget cost:** spend for a closed day (one hour after it ends, in the budget's time zone) is
  cached in Valkey for 40 days, one entry per project, source, time zone and day, shared by every
  budget; each evaluation then queries only the open days. Spend arriving later for a closed day
  isn't counted. LLM-app days cache tokens, priced on read, so a price change applies to past days.
  Every budget query has the `QUERY_MAX_SCAN_GB` limit; over it, the rule shows an error instead of
  alerting.
- **LLM apps' "user"** is the span's `user.id` (or `enduser.id`); calls without one are left out of
  per-user budgets. Cached tokens count within input tokens and use the cached price (the input
  price when none is set).
- **Dashboard template:** its cost stats are rolling (last 24 h, 7 days, 30 days), since PromQL has
  no calendar periods; budgets are calendar-aligned.
- **Data Sources → LLM app (OpenTelemetry):** an env file plus Python
  (`opentelemetry-instrumentation-openai-v2`) and Node.js snippets, with content capture off.

## Release

All four ship in v0.57.0, a FEATURE release, together with Synthetics' multi-step checks and
maintenance windows (docs/SYNTHETICS.md), the v0.56.2 fixes, and open-source readiness work.
