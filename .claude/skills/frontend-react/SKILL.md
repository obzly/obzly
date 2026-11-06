---
name: frontend-react
description: Obzly frontend conventions (React + TypeScript, antd v6, Tailwind v4, FSD layout). Use when writing or changing code under frontend/src.
---

# Obzly frontend

## Layout (Feature-Sliced Design)
- `entity/<name>/` - API modules (`api/*Api.ts`) and types (`model/*.ts`), re-exported from `index.ts`.
- `features/<name>/ui|lib/` - feature components and logic.
- `widgets/` - compositions of features (e.g. `MainScreenComponent`).
- `pages/` - routed pages. `shared/` - cross-cutting code (`site`, `theme`, `lib`, `ui`).

## API calls
- Site data (projects, logs, traces, metrics, dashboards...): `const baseUrl = () => \`${getApiBase()}/...\`` so it goes through the control plane's `/sites/{id}` proxy in multi-site mode.
- Identity and account calls (users, MFA, SSO, sites and grants): `${getApplicationServer()}/api/v1/...`.
- Use `apiHelper` + `RequestOptions` like the existing modules. See the `site-scoped-api` skill before adding a new backend route.

## Component shape
```tsx
interface Props { projectId: string }

const helper = () => { ... };

export const ThingComponent = ({ projectId }: Props): JSX.Element => {
  const { message } = App.useApp();   // antd feedback - never the static `message`
  // 1. state   2. functions   3. effects   4. derived values
  return <div>...</div>;
};
```

## Look and feel
- Primary color is Tailwind emerald; primary buttons and accents `emerald-600` (`#009966`, set as antd `colorPrimary`).
- Loader: `<Spin indicator={<LoadingOutlined spin />} />`.
- Every color class needs a dark variant (`text-gray-600 dark:text-gray-300`); the theme toggles the `.dark` class.
- Give the user feedback for every action that changes state (`message.success` / an inline `Alert` on error).
- Comments: short, only the *why*.

## Before finishing
```bash
cd frontend && npm run lint && npm run format && npm run build
```
For UI behavior changes, check them in a real browser (headless Playwright against a local container) rather than relying on the build alone.
