import { describe, expect, it } from 'vitest';

import type { AiBudget, AiBudgetStatus } from '../../../entity/aiUsage';
import type { RuleActivityResponse } from '../../../entity/alerting';
import type { Project } from '../../../entity/projects';
import {
  aiBudgetGauge,
  alertNoiseGauge,
  hotStorageGauge,
  logQuotaGauge,
  logSizeLimitMb,
} from './capacity';

const GB = 1024 ** 3;

const project = (overrides: Partial<Project> = {}): Project => ({
  id: 'p',
  name: 'Contoso Shop',
  createdAt: new Date(),
  isApiKeyRequired: false,
  isFilterByDomain: false,
  isFilterByIp: false,
  allowedDomains: [],
  allowedIps: [],
  isRateLimitingEnabled: false,
  logsPerSecondLimit: 0,
  maxLogsAmount: 0,
  maxLogsSizeMb: 0,
  maxLogsLifeDays: 30,
  maxLogSizeKb: 0,
  ...overrides,
});

const stats = (totalSizeMb: number, objectStorageSizeMb = 0) => ({
  totalLogs: 1,
  totalSizeMb,
  rawSizeMb: totalSizeMb,
  objectStorageSizeMb,
  oldestLogTime: '',
  newestLogTime: '',
});

describe('logSizeLimitMb', () => {
  it('uses the project limit only with its limits on, and the lower of it and the plan', () => {
    expect(logSizeLimitMb(project({ maxLogsSizeMb: 500 }))).toBeUndefined();
    expect(logSizeLimitMb(project({ isRateLimitingEnabled: true, maxLogsSizeMb: 500 }))).toBe(500);
    expect(
      logSizeLimitMb(
        project({
          isRateLimitingEnabled: true,
          maxLogsSizeMb: 500,
          orgPlan: {
            logsPerSecondLimit: 0,
            maxLogsAmount: 0,
            maxLogsSizeMb: 200,
            maxLogsLifeDays: 0,
            maxLogSizeKb: 0,
            maxProjects: 0,
            maxSites: 0,
            maxMembers: 0,
          },
        }),
      ),
    ).toBe(200);
  });
});

describe('logQuotaGauge', () => {
  it('says there is no limit instead of drawing zero', () => {
    const gauge = logQuotaGauge(project(), stats(100));
    expect(gauge.ratio).toBeNull();
    expect(gauge.value).toBe('n/a');
  });

  it('warns near the limit', () => {
    const gauge = logQuotaGauge(
      project({ isRateLimitingEnabled: true, maxLogsSizeMb: 1000 }),
      stats(850),
    );
    expect(gauge.ratio).toBeCloseTo(0.85);
    expect(gauge.value).toBe('85%');
    expect(gauge.pill.tone).toBe('warn');
  });
});

describe('hotStorageGauge', () => {
  it('measures what is still on local disk against the disk', () => {
    const gauge = hotStorageGauge(
      stats(30 * 1024, 10 * 1024),
      {
        totalSpans: 1,
        totalSizeMb: 5 * 1024,
        rawSizeMb: 0,
        objectStorageSizeMb: 5 * 1024,
        oldestSpanTime: '',
        newestSpanTime: '',
      },
      {
        platform: 'linux' as never,
        totalSpaceBytes: 200 * GB,
        usedSpaceBytes: 50 * GB,
        freeSpaceBytes: 150 * GB,
      },
    );
    expect(gauge.ratio).toBeCloseTo(0.1);
    expect(gauge.value).toBe('10%');
    expect(gauge.caption).toBe('of 200 GB');
    expect(gauge.pill.tone).toBe('good');
  });
});

describe('aiBudgetGauge', () => {
  const budget = (id: string, thresholds: number[] = [80]): AiBudget => ({
    id,
    projectId: 'p',
    name: `Budget ${id}`,
    amountUsd: 100,
    period: 'month',
    timeZone: 'UTC',
    scope: 'project',
    scopeValue: '',
    thresholds,
    channelIds: [],
    includeClaudeCode: true,
    includeLlmApps: true,
    createdAt: '',
    updatedAt: '',
  });
  const status = (budgetId: string, percent: number): AiBudgetStatus => ({
    budgetId,
    periodStart: 0,
    periodEnd: 0,
    spentUsd: percent,
    percent,
    forecastUsd: 0,
    groups: 1,
    groupsWarning: 0,
    groupsOver: 0,
    top: [],
  });

  it('shows the budget closest to its limit', () => {
    const gauge = aiBudgetGauge([budget('a'), budget('b')], [status('a', 40), status('b', 85)]);
    expect(gauge.value).toBe('85%');
    expect(gauge.pill).toEqual({ text: 'near limit', tone: 'warn' });
  });

  it('is n/a without a budget', () => {
    expect(aiBudgetGauge([], []).ratio).toBeNull();
  });
});

describe('alertNoiseGauge', () => {
  const activity = (fired: number, notifications: number): RuleActivityResponse => ({
    days: ['2026-11-12', '2026-11-18'],
    trackedSince: null,
    rules: [
      {
        ruleId: 'r',
        fired: [],
        notifications: [],
        firedTotal: fired,
        notificationsTotal: notifications,
      },
    ],
  });

  it('divides notifications by firings', () => {
    const gauge = alertNoiseGauge(activity(4, 34));
    expect(gauge.value).toBe('8.5');
    expect(gauge.pill.tone).toBe('warn');
  });

  it('is n/a, not 0, when nothing fired', () => {
    expect(alertNoiseGauge(activity(0, 0)).value).toBe('n/a');
  });
});
