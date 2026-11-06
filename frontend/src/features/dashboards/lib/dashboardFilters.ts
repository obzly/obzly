import type { Panel } from '../../../entity/dashboards';

// Dashboard filter bar (Namespace/Pod/Node/Service): applied to a copy of each panel right
// before its query runs, never saved. No Deployment filter: kubeletstats metrics carry no
// k8s.deployment.name label.
export interface DashboardFilters {
  namespace?: string;
  pod?: string;
  node?: string;
  service?: string;
}

export const EMPTY_FILTERS: DashboardFilters = {};

export function hasActiveFilters(filters: DashboardFilters): boolean {
  return Boolean(filters.namespace || filters.pod || filters.node || filters.service);
}

// Metric-name prefixes that carry each k8s label. A matcher on a metric without that label
// would match no series and empty an unrelated panel.
const POD_SCOPED_METRIC_PREFIXES = ['k8s_pod_', 'k8s_volume_'];
const NODE_SCOPED_METRIC_PREFIXES = ['k8s_node_'];

// Grouping/matching clauses hold label names (k8s_pod_name looks like a
// pod metric) - masked while selectors are rewritten.
const LABEL_LIST_CLAUSE = /\b(by|without|on|ignoring|group_left|group_right)\s*\([^)]*\)/g;

function escapeForPromQLString(value: string): string {
  return value.replace(/\\/g, '\\\\').replace(/"/g, '\\"');
}

function escapeRegExp(value: string): string {
  return value.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
}

/**
 * Adds label="value" to every selector of a metric with one of the prefixes:
 * bare `name` / `name{...}` and `{__name__=~"prefix..."}` selectors alike.
 */
export function injectLabelMatcher(
  promQuery: string,
  prefixes: string[],
  label: string,
  value: string,
): string {
  const matcher = `${label}="${escapeForPromQLString(value)}"`;
  const masked: string[] = [];
  let query = promQuery.replace(LABEL_LIST_CLAUSE, (clause) => {
    masked.push(clause);
    return `\uE000${masked.length - 1}\uE000`;
  });

  query = query.replace(
    /\{(\s*__name__\s*=~?\s*"([^"]*)"[^}]*)\}/g,
    (all, inner: string, name: string) =>
      prefixes.some((prefix) => name.startsWith(prefix)) ? `{${inner}, ${matcher}}` : all,
  );

  const bare = new RegExp(
    // Not a label name inside braces: those are followed by =, !=, =~ or !~.
    `(^|[^"\\w:])((?:${prefixes.map(escapeRegExp).join('|')})[a-zA-Z0-9_:]*)(?![a-zA-Z0-9_:])(?!\\s*(?:=(?!=)|!=|!~))(\\{[^}]*\\})?`,
    'g',
  );
  query = query.replace(bare, (_all, before: string, name: string, braces?: string) => {
    if (!braces) return `${before}${name}{${matcher}}`;
    const inner = braces.slice(1, -1).trim();
    return `${before}${name}{${inner ? `${inner}, ` : ''}${matcher}}`;
  });

  return query.replace(/\uE000(\d+)\uE000/g, (_all, index: string) => masked[Number(index)]);
}

export function applyFiltersToPanel(panel: Panel, filters: DashboardFilters): Panel {
  if (panel.source === 'spans') {
    if (filters.service && !panel.serviceName) {
      return { ...panel, serviceName: filters.service };
    }
    return panel;
  }

  if (panel.source !== 'metrics' || !panel.promQuery) {
    return panel;
  }

  let query = panel.promQuery;
  if (filters.namespace) {
    query = injectLabelMatcher(
      query,
      POD_SCOPED_METRIC_PREFIXES,
      'k8s_namespace_name',
      filters.namespace,
    );
  }
  if (filters.pod) {
    query = injectLabelMatcher(query, POD_SCOPED_METRIC_PREFIXES, 'k8s_pod_name', filters.pod);
  }
  if (filters.node) {
    query = injectLabelMatcher(query, NODE_SCOPED_METRIC_PREFIXES, 'k8s_node_name', filters.node);
  }

  return query === panel.promQuery ? panel : { ...panel, promQuery: query };
}
