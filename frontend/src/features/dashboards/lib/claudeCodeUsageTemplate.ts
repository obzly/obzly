import type { Panel, Tab, Variable } from '../../../entity/dashboards';

// Built on Claude Code's own OTel metrics (claude_code.cost.usage and the rest). They need the
// metrics exporter the Data Sources snippet turns on; with logs only these panels stay empty.
// app_version is sent only when Claude Code is configured to include it.

export const claudeCodeUsageVariables: Variable[] = [
  { key: 'user', label: 'User', source: 'metricLabel', metricLabelName: 'user_email' },
  { key: 'model', label: 'Model', source: 'metricLabel', metricLabelName: 'model' },
  { key: 'terminal', label: 'Terminal', source: 'metricLabel', metricLabelName: 'terminal_type' },
  { key: 'version', label: 'Version', source: 'metricLabel', metricLabelName: 'app_version' },
];

// Unset variables become .* (dashboardVariables.ts). Not every metric has
// a model label (sessions, commits, active time): `model=~".*"` matches a
// missing label, so a set model only narrows the metrics that carry one.
const SEL = 'user_email=~"${user}", terminal_type=~"${terminal}", app_version=~"${version}"';
const WITH_MODEL = `${SEL}, model=~"\${model}"`;

const cost = (window: string) => `sum(increase(claude_code_cost_usage{${WITH_MODEL}}[${window}]))`;
const tokens = (types: string, window: string) =>
  `sum(increase(claude_code_token_usage{${WITH_MODEL}, type=~"${types}"}[${window}]))`;

function panel(
  title: string,
  type: Panel['type'],
  promQuery: string,
  gridPos: Panel['gridPos'],
  unit?: Panel['unit'],
): Panel {
  return { id: crypto.randomUUID(), title, type, source: 'metrics', promQuery, unit, gridPos };
}

function usageTab(): Tab {
  return {
    id: crypto.randomUUID(),
    title: 'Claude Code usage',
    panels: [
      panel('Cost, last 24 h (USD)', 'stat', cost('1d'), { x: 0, y: 0, w: 2, h: 4 }),
      panel('Cost, last 7 days (USD)', 'stat', cost('7d'), { x: 2, y: 0, w: 2, h: 4 }),
      panel('Cost, last 30 days (USD)', 'stat', cost('30d'), { x: 4, y: 0, w: 2, h: 4 }),
      panel(
        'Cache hit ratio (7 days)',
        'stat',
        `${tokens('cacheRead', '7d')} / ${tokens('input|cacheRead|cacheCreation', '7d')}`,
        { x: 6, y: 0, w: 2, h: 4 },
        'percent',
      ),
      panel('Sessions, last 24 h', 'stat', `sum(increase(claude_code_session_count{${SEL}}[1d]))`, {
        x: 8,
        y: 0,
        w: 2,
        h: 4,
      }),
      panel(
        'Commits (7 days)',
        'stat',
        `sum(increase(claude_code_commit_count{${SEL}}[7d])) or vector(0)`,
        { x: 10, y: 0, w: 1, h: 4 },
      ),
      panel(
        'Pull requests (7 days)',
        'stat',
        `sum(increase(claude_code_pull_request_count{${SEL}}[7d])) or vector(0)`,
        { x: 11, y: 0, w: 1, h: 4 },
      ),
      panel(
        'Cost by user (USD per hour)',
        'bar',
        `sum by (user_email) (increase(claude_code_cost_usage{${WITH_MODEL}}[1h]))`,
        { x: 0, y: 4, w: 6, h: 8 },
      ),
      panel(
        'Cost by model (USD per hour)',
        'bar',
        `sum by (model) (increase(claude_code_cost_usage{${WITH_MODEL}}[1h]))`,
        { x: 6, y: 4, w: 6, h: 8 },
      ),
      panel(
        'Tokens by type (per hour)',
        'bar',
        `sum by (type) (increase(claude_code_token_usage{${WITH_MODEL}}[1h]))`,
        { x: 0, y: 12, w: 6, h: 8 },
      ),
      panel(
        'Cache hit ratio',
        'line',
        `${tokens('cacheRead', '1h')} / ${tokens('input|cacheRead|cacheCreation', '1h')}`,
        { x: 6, y: 12, w: 6, h: 8 },
        'percent',
      ),
      panel('Sessions per day', 'bar', `sum(increase(claude_code_session_count{${SEL}}[1d]))`, {
        x: 0,
        y: 20,
        w: 4,
        h: 8,
      }),
      panel(
        'Active time, user vs CLI (hours per day)',
        'bar',
        `sum by (type) (increase(claude_code_active_time_total{${SEL}}[1d])) / 3600`,
        { x: 4, y: 20, w: 4, h: 8 },
      ),
      panel(
        'Lines of code added and removed (per day)',
        'bar',
        `sum by (type) (increase(claude_code_lines_of_code_count{${WITH_MODEL}}[1d]))`,
        { x: 8, y: 20, w: 4, h: 8 },
      ),
      panel(
        'Commits and pull requests (per day)',
        'bar',
        `label_replace(sum(increase(claude_code_commit_count{${SEL}}[1d])), "kind", "commits", "", "")` +
          ` or label_replace(sum(increase(claude_code_pull_request_count{${SEL}}[1d])), "kind", "pull requests", "", "")`,
        { x: 0, y: 28, w: 4, h: 8 },
      ),
      panel(
        'Edit suggestions accepted, by tool and language (7 days)',
        'table',
        `sum by (tool_name, language) (increase(claude_code_code_edit_tool_decision{${SEL}, decision="accept"}[7d]))` +
          ` / sum by (tool_name, language) (increase(claude_code_code_edit_tool_decision{${SEL}}[7d]))`,
        { x: 4, y: 28, w: 4, h: 8 },
        'percent',
      ),
      panel(
        'Top 10 users by cost (30 days, USD)',
        'table',
        `topk(10, sum by (user_email) (increase(claude_code_cost_usage{${WITH_MODEL}}[30d])))`,
        { x: 8, y: 28, w: 4, h: 8 },
      ),
    ],
  };
}

export function claudeCodeUsageTabs(): Tab[] {
  return [usageTab()];
}
