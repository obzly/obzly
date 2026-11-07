# Obzly frontend

React + TypeScript + Vite single-page app, served by every Obzly process that serves HTTP (`all`,
`api`, `control-plane`). Ant Design and Tailwind for UI, ECharts for charts, CodeMirror for the
SQL editor.

## Develop

```bash
npm install
npm run dev      # Vite on :5173, calling the backend on the same host at :4005
```

Run the backend alongside it (`cd ../backend && make run`, see `../backend/README.md`). In
development the app always calls `http://<host>:4005`; in production it calls its own origin.

Before committing:

```bash
npm run lint && npm run format && npm test && npm run build
```

## Test

- `npm test` - Vitest unit tests (`src/**/*.test.ts`), run in CI: the search bar parser, attribute
  name normalization (it must match the backend's), HTML escaping, log level ordering.
- `npm run test:e2e` - Playwright smoke tests (`e2e/`) against a running Obzly: sign in, run a
  search, open the main tabs, and fail on any uncaught page error. Set `OBZLY_URL`, `OBZLY_EMAIL`
  and `OBZLY_PASSWORD` (a non-admin account - admins need MFA); skipped when unset. Browsers come
  from `npx playwright install`, or run it in `mcr.microsoft.com/playwright:v1.63.0-noble`.

## Live demo

`npm run dev:demo` runs the UI with no backend at all, and `npm run build:demo` builds the live demo
published at [demo.obzly.com](https://demo.obzly.com) by `.github/workflows/demo.yml` (on every
release tag, or by hand). In demo mode `src/demo/` patches `fetch` and answers every `/api/v1` call
in the browser:

- Sample data for a fictional shop, Harborline (`src/demo/world.ts`), is generated from the time
  range by a seeded PRNG, so any range has consistent logs, traces and metrics, with a recurring
  incident every six hours.
- What a visitor creates or changes is kept in their browser's localStorage (`obzly_demo_v1`);
  "Reset demo" clears it. Nothing is sent anywhere.
- SQL mode runs a real DuckDB (WebAssembly, loaded on first use); PromQL is a built-in subset.
- Each area's routes live in `src/demo/handlers/<area>.ts`. `src/demo/coverage.test.ts` fails when
  an `entity/*/api` module calls a URL with no demo handler: add one (or a `notInDemo()` stub) when
  you add an API call.

Production builds don't contain any of it: `main.tsx` imports `src/demo` only when the build-time
constant `__OBZLY_DEMO__` is true (`vite --mode demo`).

## Layout (Feature-Sliced Design)

- `src/entity/<name>/` - API modules (`api/*Api.ts`) and types (`model/*.ts`).
- `src/features/<name>/` - feature UI and logic (logs search, traces, dashboards, alerts,
  synthetics, infrastructure with cloud connections, organizations, settings, ...).
- `src/widgets/` - compositions of features (the main screen).
- `src/pages/` - routed pages. `src/shared/` - cross-cutting code (site, theme, lib, ui).

Site data (logs, traces, metrics, dashboards) goes through `getApiBase()`, so that in multi-site
mode it is routed through the control plane's `/sites/{id}` proxy; identity and account calls use
`getApplicationServer()`. Runtime settings (`IS_CLOUD`, OAuth client ids, `IS_CONTROL_PLANE`) come
from `window.__RUNTIME_CONFIG__`, written by the server, so one build serves every role.

Logs search pages with the server's `nextCursor` (Load More) and shows a skipped count
(`countSkipped`) when a query would read more than the Site's scan budget.

Project conventions: `.claude/skills/frontend-react` at the repo root.
