import type { AlertRule, RuleRequest } from '../../../entity/alerting';
import type { PillTone } from './bits';

export const BUDGET_TIP = 'Kept by an AI budget; change it in AI Observability, Budgets.';
export const SYNTHETICS_TIP =
  "Kept by a Synthetics monitor's alerts in the control plane; change it there.";
export const COST_TIP =
  'Kept by Cost (a budget, anomalies or commitments ending); change it there.';
export const POSTURE_TIP = "Kept by Posture's finding alerts; change it in Posture.";

export interface ManagedRuleInfo {
  label: string;
  tone: PillTone;
  tip: string;
}

/** Which feature keeps a managed rule (by its managedBy prefix), or undefined. */
export const managedRuleInfo = (
  rule: Pick<AlertRule, 'managedBy'>,
): ManagedRuleInfo | undefined => {
  const managedBy = rule.managedBy;
  if (!managedBy) return undefined;
  if (managedBy.startsWith('ai-budget:'))
    return { label: 'Budget', tone: 'purple', tip: BUDGET_TIP };
  if (managedBy.startsWith('cloud-cost:')) return { label: 'Cost', tone: 'muted', tip: COST_TIP };
  if (managedBy.startsWith('cloud-posture:'))
    return { label: 'Posture', tone: 'muted', tip: POSTURE_TIP };
  return { label: 'Synthetics', tone: 'info', tip: SYNTHETICS_TIP };
};

export const errorMessage = (error: unknown, fallback: string) =>
  error instanceof Error ? error.message : fallback;

export const toRequest = (rule: AlertRule): RuleRequest => ({
  name: rule.name,
  enabled: rule.enabled,
  source: rule.source,
  query: rule.query,
  comparison: rule.comparison,
  threshold: rule.threshold,
  windowSeconds: rule.windowSeconds,
  intervalSeconds: rule.intervalSeconds,
  forSeconds: rule.forSeconds,
  repeatSeconds: rule.repeatSeconds,
  labels: rule.labels,
  summary: rule.summary,
  channelIds: rule.channelIds,
  severity: rule.severity,
  description: rule.description,
});

/** A logs_sql rule's SQL on one short line, for the rules list. */
export const ruleSqlSummary = (sql: string, maxChars = 60): string => {
  const line = sql.replace(/\s+/g, ' ').trim();
  return line.length > maxChars ? `${line.slice(0, maxChars - 1)}…` : line;
};
