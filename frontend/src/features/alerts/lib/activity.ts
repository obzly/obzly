import type { AlertRule, AlertsSummary, RuleActivityResponse } from '../../../entity/alerting';

/** One day of a rule's activity; null values are unknown (not counted yet). */
export interface ActivityDay {
  day: string;
  fired: number | null;
  notifications: number | null;
}

export interface NoisyRule {
  ruleId: string;
  name: string;
  notifications: number;
  fired: number;
  /** Notifications per firing; null when it never fired (e.g. reminders only). */
  perFiring: number | null;
}

/** The UTC day (YYYY-MM-DD) of an ISO timestamp. */
const utcDay = (iso: string): string => new Date(iso).toISOString().slice(0, 10);

/**
 * A rule's days: days before the Site started counting, or before the rule existed, are
 * unknown rather than zero.
 */
export const ruleDays = (
  activity: RuleActivityResponse,
  rule: Pick<AlertRule, 'id' | 'createdAt'>,
): ActivityDay[] => {
  const counts = activity.rules.find((r) => r.ruleId === rule.id);
  const firstKnown = [activity.trackedSince, rule.createdAt]
    .filter((t): t is string => !!t)
    .map(utcDay)
    .sort()
    .pop();
  return activity.days.map((day, i) => {
    const known = !!counts && (!firstKnown || day >= firstKnown);
    return {
      day,
      fired: known ? counts.fired[i] : null,
      notifications: known ? counts.notifications[i] : null,
    };
  });
};

/** Rules that sent notifications in the window, most first; ties by firings, then name. */
export const noisiestRules = (
  activity: RuleActivityResponse,
  rules: Pick<AlertRule, 'id' | 'name'>[],
  limit = 5,
): NoisyRule[] => {
  const names = new Map(rules.map((r) => [r.id, r.name]));
  return activity.rules
    .filter((r) => r.notificationsTotal > 0 && names.has(r.ruleId))
    .map((r) => ({
      ruleId: r.ruleId,
      name: names.get(r.ruleId) ?? r.ruleId,
      notifications: r.notificationsTotal,
      fired: r.firedTotal,
      perFiring: r.firedTotal > 0 ? r.notificationsTotal / r.firedTotal : null,
    }))
    .sort(
      (a, b) =>
        b.notifications - a.notifications || b.fired - a.fired || a.name.localeCompare(b.name),
    )
    .slice(0, limit);
};

const plural = (n: number, one: string, many: string) =>
  `${n.toLocaleString()} ${n === 1 ? one : many}`;

/** "3 firing · 7 rules · 2 silences", with pending alerts when there are any. */
export const summaryLine = (summary: AlertsSummary): string =>
  [
    `${summary.firing.toLocaleString()} firing`,
    ...(summary.pending > 0 ? [`${summary.pending.toLocaleString()} pending`] : []),
    plural(summary.rules, 'rule', 'rules'),
    plural(summary.silences, 'silence', 'silences'),
  ].join(' · ');
