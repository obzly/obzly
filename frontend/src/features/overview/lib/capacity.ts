// The Overview's budgets and capacity gauges, from what each API returned. A gauge with nothing
// to measure against says so ("no limit", "no budget") instead of drawing an empty arc as zero.
import type { AiBudget, AiBudgetStatus } from '../../../entity/aiUsage';
import type { RuleActivityResponse } from '../../../entity/alerting';
import type { DiskUsage } from '../../../entity/disk';
import type { Project } from '../../../entity/projects';
import type { ProjectLogStats as LogsStats } from '../../../entity/query';
import type { SpansStats } from '../../../entity/traces';

export type PillTone = 'good' | 'warn' | 'bad' | 'neutral';

export interface GaugeModel {
  /** 0..1, or null when there is nothing to measure against. */
  ratio: number | null;
  value: string;
  caption: string;
  foot: string;
  pill: { text: string; tone: PillTone };
}

const MB = 1024 * 1024;
const GB = 1024 * MB;

const formatGb = (bytes: number): string => {
  const gb = bytes / GB;
  return gb >= 100 ? gb.toFixed(0) : gb >= 10 ? gb.toFixed(1) : gb.toFixed(2);
};

const formatUsd = (amount: number): string =>
  `$${amount.toLocaleString('en-US', { maximumFractionDigits: amount >= 100 ? 0 : 2 })}`;

/** The AI budget closest to its limit (the per-user/per-model ones by their highest spender). */
export const aiBudgetGauge = (budgets: AiBudget[], statuses: AiBudgetStatus[]): GaugeModel => {
  const byId = new Map(budgets.map((budget) => [budget.id, budget]));
  const known = statuses.filter((status) => !status.error && byId.has(status.budgetId));
  if (known.length === 0) {
    return {
      ratio: null,
      value: 'n/a',
      caption: budgets.length ? 'not measured' : 'no budget',
      foot: budgets.length
        ? 'Budget spend could not be measured'
        : 'Set a spend budget in AI observability',
      pill: { text: budgets.length ? 'unknown' : 'none set', tone: 'neutral' },
    };
  }
  const worst = known.reduce((a, b) => (b.percent > a.percent ? b : a));
  const budget = byId.get(worst.budgetId)!;
  const warnAt = budget.thresholds.length ? Math.min(...budget.thresholds) : 80;
  const tone: PillTone = worst.percent >= 100 ? 'bad' : worst.percent >= warnAt ? 'warn' : 'good';
  return {
    ratio: worst.percent / 100,
    value: `${Math.round(worst.percent)}%`,
    caption: 'used',
    foot: `${budget.name}: ${formatUsd(worst.spentUsd)} of ${formatUsd(budget.amountUsd)} this ${budget.period}`,
    pill: { text: tone === 'bad' ? 'over' : tone === 'warn' ? 'near limit' : 'on track', tone },
  };
};

/**
 * The stored-logs limit that applies: the project's own (only with its limits on; 0 is
 * unlimited) and the organization plan's (always enforced), whichever is lower.
 */
export const logSizeLimitMb = (project: Project): number | undefined => {
  const limits = [
    project.isRateLimitingEnabled ? project.maxLogsSizeMb : 0,
    project.orgPlan?.maxLogsSizeMb ?? 0,
  ].filter((value) => value > 0);
  return limits.length ? Math.min(...limits) : undefined;
};

export const logQuotaGauge = (project: Project, stats: LogsStats): GaugeModel => {
  const limit = logSizeLimitMb(project);
  const used = `${formatGb(stats.totalSizeMb * MB)} GB stored`;
  if (limit === undefined) {
    return {
      ratio: null,
      value: 'n/a',
      caption: 'no limit',
      foot: `${used}, no size limit set`,
      pill: { text: 'unlimited', tone: 'neutral' },
    };
  }
  const ratio = stats.totalSizeMb / limit;
  // The quota worker deletes the oldest logs past the limit, so near it data starts going.
  const tone: PillTone = ratio >= 0.95 ? 'bad' : ratio >= 0.8 ? 'warn' : 'good';
  return {
    ratio,
    value: `${Math.round(ratio * 100)}%`,
    caption: 'used',
    foot: `${formatGb(stats.totalSizeMb * MB)} of ${formatGb(limit * MB)} GB`,
    pill: {
      text: tone === 'good' ? 'room left' : tone === 'warn' ? 'near limit' : 'trimming',
      tone,
    },
  };
};

/** Logs and spans still on local disk (not yet in object storage), against the disk. */
export const hotStorageGauge = (
  logs: LogsStats | undefined,
  spans: SpansStats | undefined,
  disk: DiskUsage,
): GaugeModel => {
  if (!logs || !disk.totalSpaceBytes) {
    return {
      ratio: null,
      value: 'n/a',
      caption: 'unknown',
      foot: 'Storage figures are not available',
      pill: { text: 'unknown', tone: 'neutral' },
    };
  }
  const localMb =
    logs.totalSizeMb -
    (logs.objectStorageSizeMb ?? 0) +
    (spans ? spans.totalSizeMb - (spans.objectStorageSizeMb ?? 0) : 0);
  const remoteMb = (logs.objectStorageSizeMb ?? 0) + (spans?.objectStorageSizeMb ?? 0);
  const ratio = Math.max(0, localMb * MB) / disk.totalSpaceBytes;
  const diskUsed = disk.usedSpaceBytes / disk.totalSpaceBytes;
  const tone: PillTone = diskUsed >= 0.95 ? 'bad' : diskUsed >= 0.8 ? 'warn' : 'good';
  return {
    ratio,
    value: `${ratio < 0.01 && localMb > 0 ? '<1' : Math.round(ratio * 100)}%`,
    caption: `of ${formatGb(disk.totalSpaceBytes)} GB`,
    foot: `${formatGb(localMb * MB)} GB local · ${formatGb(remoteMb * MB)} GB in object storage${spans ? '' : ' (logs only)'}`,
    pill: {
      text: tone === 'good' ? 'disk healthy' : `disk ${Math.round(diskUsed * 100)}% full`,
      tone,
    },
  };
};

/** Notifications sent per alert firing over the days the Site counted. */
export const alertNoiseGauge = (activity: RuleActivityResponse): GaugeModel => {
  const fired = activity.rules.reduce((sum, rule) => sum + rule.firedTotal, 0);
  const sent = activity.rules.reduce((sum, rule) => sum + rule.notificationsTotal, 0);
  const days = activity.days.length;
  const since =
    activity.trackedSince &&
    activity.days[0] &&
    activity.trackedSince.slice(0, 10) > activity.days[0]
      ? `since ${activity.trackedSince.slice(0, 10)}`
      : `${days} days`;
  if (activity.rules.length === 0) {
    return {
      ratio: null,
      value: 'n/a',
      caption: 'no rules',
      foot: 'No alert rules in this project',
      pill: { text: 'none', tone: 'neutral' },
    };
  }
  if (fired === 0) {
    return {
      ratio: null,
      value: 'n/a',
      caption: 'no firings',
      foot: `${sent} notifications, nothing fired · ${since}`,
      pill: { text: 'quiet', tone: 'good' },
    };
  }
  const perFiring = sent / fired;
  const tone: PillTone = perFiring > 5 ? 'warn' : 'good';
  return {
    // Ten a firing fills the arc.
    ratio: Math.min(perFiring / 10, 1),
    value: perFiring.toFixed(1),
    caption: 'per firing',
    foot: `${sent} notifications for ${fired} firings · ${since}`,
    pill: { text: tone === 'good' ? 'healthy' : 'review rules', tone },
  };
};
