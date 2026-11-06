import type { AlertRule, RuleRequest } from '../../../entity/alerting';

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
});
