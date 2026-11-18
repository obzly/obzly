import { describe, expect, it } from 'vitest';

import type { CostBudget, CostRecommendation, ExploreResponse } from '../../../entity/cost';
import type { PostureAction, PostureSummary } from '../../../entity/posture';
import {
  basisForecast,
  basisTotals,
  commitmentCoverage,
  dailyRange,
  monthProgress,
  nextActions,
  openSavings,
  realizedSavings,
  sameDaysLastMonth,
  serviceRows,
  severityChanges,
  standardRows,
  topAnomaly,
  totalBudget,
} from './summary';

const recommendation = (overrides: Partial<CostRecommendation>): CostRecommendation => ({
  key: 'k',
  projectId: 'p',
  connectionName: 'Contoso-Payer',
  provider: 'aws',
  source: 'compute_optimizer',
  kind: 'rightsizing',
  account: '111122223333',
  region: 'eu-west-1',
  resourceKey: 'i-0abc',
  resourceName: 'checkout-1',
  resourceType: 'ec2',
  action: 'Rightsize',
  currency: 'USD',
  monthlySavings: 100,
  savingsBasis: 'provider',
  effort: 'low',
  risk: 'low',
  evidence: {},
  status: 'open',
  ...overrides,
});

describe('month ranges', () => {
  it('compares with the same days of last month, clamped to its length', () => {
    expect(sameDaysLastMonth('2026-11-18')).toEqual({ from: '2026-10-01', to: '2026-10-18' });
    expect(sameDaysLastMonth('2026-03-31')).toEqual({ from: '2026-02-01', to: '2026-02-28' });
    expect(sameDaysLastMonth('2026-01-10')).toEqual({ from: '2025-12-01', to: '2025-12-10' });
    expect(dailyRange('2026-11-18')).toEqual({ from: '2026-10-01', to: '2026-11-18' });
  });

  it('knows the day of the month and its length', () => {
    expect(monthProgress('2026-11-18')).toEqual({ day: 18, days: 30 });
    expect(monthProgress('2028-02-03')).toEqual({ day: 3, days: 29 });
  });
});

describe('basisTotals', () => {
  const explore: ExploreResponse = {
    periods: [
      { key: '2026-10-01', from: '2026-10-01', to: '2026-10-01', coverage: 'full' },
      { key: '2026-10-02', from: '2026-10-02', to: '2026-10-02', coverage: 'full' },
      { key: '2026-10-03', from: '2026-10-03', to: '2026-10-03', coverage: 'full' },
      { key: '2026-11-01', from: '2026-11-01', to: '2026-11-01', coverage: 'full' },
      { key: '2026-11-02', from: '2026-11-02', to: '2026-11-02', coverage: 'partial' },
    ],
    currencies: [
      {
        currency: 'USD',
        rows: [
          { key: 'aws', values: [10, 10, 99, 20, 30], total: 169 },
          { key: 'azure', values: [5, 5, 99, 5, 10], total: 124 },
        ],
        totals: [15, 15, 198, 25, 40],
        total: 293,
      },
    ],
  };

  it('adds up this month and the same days of last month, stacked by provider', () => {
    const totals = basisTotals(explore, 'USD', '2026-11-02', new Set(['2026-11-02']))!;
    expect(totals.monthToDate).toBe(65);
    expect(totals.sameDaysLastMonth).toBe(30);
    expect(totals.providers).toEqual(['aws', 'azure']);
    expect(totals.days).toEqual([
      { day: '2026-11-01', amounts: { aws: 20, azure: 5 }, total: 25, estimated: false },
      { day: '2026-11-02', amounts: { aws: 30, azure: 10 }, total: 40, estimated: true },
    ]);
  });

  it('has nothing for a currency it did not return', () => {
    expect(basisTotals(explore, 'EUR', '2026-11-02')).toBeUndefined();
  });
});

describe('basisForecast', () => {
  it("adds the providers' remaining forecast to this basis's month to date", () => {
    expect(basisForecast(3000, 1000, 1100)).toBe(3100);
    expect(basisForecast(undefined, 1000, 1100)).toBeUndefined();
  });
});

describe('serviceRows', () => {
  it('pairs this month with the same days of last month', () => {
    const now: ExploreResponse = {
      periods: [],
      currencies: [
        {
          currency: 'USD',
          rows: [
            { key: 'Amazon EC2', values: [], total: 120 },
            { key: 'Amazon S3', values: [], total: 30 },
          ],
          totals: [],
          total: 150,
        },
      ],
    };
    const before: ExploreResponse = {
      periods: [],
      currencies: [
        {
          currency: 'USD',
          rows: [{ key: 'Amazon EC2', values: [], total: 100 }],
          totals: [],
          total: 100,
        },
      ],
    };
    expect(serviceRows(now, before, 'USD')).toEqual([
      { key: 'Amazon EC2', label: 'Amazon EC2', total: 120, change: 20 },
      { key: 'Amazon S3', label: 'Amazon S3', total: 30, change: undefined },
    ]);
  });
});

describe('savings', () => {
  const items = [
    recommendation({ key: 'a', monthlySavings: 300 }),
    recommendation({ key: 'b', kind: 'idle', monthlySavings: 50, status: 'planned' }),
    recommendation({ key: 'c', monthlySavings: null }),
    recommendation({ key: 'd', currency: 'EUR', monthlySavings: 999 }),
    recommendation({ key: 'e', status: 'done', expectedMonthly: 200, realizedMonthly: 150 }),
    recommendation({ key: 'f', status: 'done', realizedMonthly: null }),
  ];

  it('adds up open savings in one currency, counting unpriced ones apart', () => {
    expect(openSavings(items, 'USD')).toEqual({
      total: 350,
      count: 3,
      unknown: 1,
      topKind: 'rightsizing',
    });
  });

  it('compares realized savings with what was expected', () => {
    expect(realizedSavings(items, 'USD')).toEqual({
      realized: 150,
      expected: 200,
      measured: 1,
      pending: 1,
    });
  });
});

describe('totalBudget', () => {
  const budget = (overrides: Partial<CostBudget>): CostBudget => ({
    id: 'b',
    projectId: 'p',
    name: 'Monthly',
    filters: {},
    currency: 'USD',
    amount: 1000,
    thresholdPercent: 90,
    channelIds: [],
    spent: 0,
    projected: 0,
    percent: 0,
    status: 'ok',
    method: 'forecast',
    ...overrides,
  });

  it('adds whole-account budgets and ignores filtered ones', () => {
    expect(
      totalBudget(
        [
          budget({}),
          budget({ id: 'c', amount: 500 }),
          budget({ id: 'd', filters: { service: ['Amazon EC2'] } }),
        ],
        'USD',
      ),
    ).toEqual({ amount: 1500, count: 2, thresholdPercent: 90 });
    expect(totalBudget([budget({ filters: { provider: ['aws'] } })], 'USD')).toBeUndefined();
  });
});

describe('topAnomaly', () => {
  it('picks the open anomaly this month that cost the most over expected', () => {
    const anomaly = (
      day: string,
      expected: number,
      actual: number,
      status: 'open' | 'acknowledged' = 'open',
    ) => ({
      id: day,
      projectId: 'p',
      connectionId: 'c',
      connectionName: 'Contoso-Payer',
      day,
      subAccount: '111122223333',
      service: 'Amazon EC2',
      currency: 'USD',
      expected,
      actual,
      score: 3,
      status,
      note: '',
      detectedAt: '',
    });
    expect(
      topAnomaly(
        [
          anomaly('2026-10-30', 10, 500),
          anomaly('2026-11-09', 600, 1000),
          anomaly('2026-11-10', 100, 300),
          anomaly('2026-11-11', 1, 900, 'acknowledged'),
        ],
        'USD',
        '2026-11-18',
      )?.day,
    ).toBe('2026-11-09');
  });
});

describe('commitmentCoverage', () => {
  it('weights by eligible spend when every row has it', () => {
    // 50% of 200 eligible and 80% of 500 eligible: 500 covered of 700.
    expect(
      commitmentCoverage([
        {
          connectionId: 'a',
          connectionName: 'a',
          type: 'compute_sp',
          coveredPercent: 50,
          onDemandMonthly: 100,
        },
        {
          connectionId: 'b',
          connectionName: 'b',
          type: 'compute_sp',
          coveredPercent: 80,
          onDemandMonthly: 100,
        },
      ]),
    ).toBeCloseTo((500 / 700) * 100);
    expect(commitmentCoverage([])).toBeUndefined();
  });
});

describe('posture', () => {
  const summary: PostureSummary = {
    standards: [
      {
        standard: 'cis-aws',
        name: 'CIS AWS 3.0',
        passed: 78,
        failed: 22,
        evaluated: 100,
        percent: 78,
        sources: ['native'],
        trend: [
          { day: '2026-11-10', percent: 75 },
          { day: '2026-11-11', percent: 76 },
          { day: '2026-11-18', percent: 78 },
        ],
      },
      {
        standard: 'none',
        name: 'Nothing',
        passed: 0,
        failed: 0,
        evaluated: 0,
        percent: 0,
        sources: [],
      },
    ],
    open: { critical: 4, high: 19 },
    suppressed: 0,
    resolved30d: 0,
    coverage: { native: 1, fallback: 0, baseline: 0, notCovered: 0, stale: 0 },
  };

  it('gives each evaluated standard its change since a week ago', () => {
    const rows = standardRows(summary, '2026-11-18');
    expect(rows).toHaveLength(1);
    expect(rows[0].weekChange).toBe(2);
  });

  it('compares open findings with the first trend period', () => {
    expect(
      severityChanges(summary, {
        periods: [
          {
            key: '',
            from: '',
            to: '',
            open: { critical: 3, high: 22 },
            opened: 0,
            resolved: 0,
            scores: {},
          },
        ],
      }),
    ).toEqual([
      { severity: 'critical', count: 4, change: 1 },
      { severity: 'high', count: 19, change: -3 },
      { severity: 'medium', count: 0, change: undefined },
      { severity: 'low', count: 0, change: undefined },
    ]);
  });
});

describe('nextActions', () => {
  const action = (
    key: string,
    rank: number,
    overrides: Partial<PostureAction> = {},
  ): PostureAction => ({
    key,
    projectId: 'p',
    kind: 'fix',
    title: key,
    effort: 'low',
    rank,
    findings: [],
    status: 'open',
    planned: false,
    dismissed: false,
    ...overrides,
  });

  it('alternates the best cost and posture items', () => {
    const result = nextActions(
      [
        recommendation({ key: 'small', monthlySavings: 10 }),
        recommendation({ key: 'big', monthlySavings: 700 }),
        recommendation({ key: 'unknown', monthlySavings: null }),
        recommendation({ key: 'done', status: 'done', monthlySavings: 9999 }),
      ],
      [action('low', 1), action('high', 9), action('dismissed', 99, { dismissed: true })],
      'USD',
      4,
    );
    expect(result.map((item) => `${item.kind}:${item.item.key}`)).toEqual([
      'cost:big',
      'posture:high',
      'cost:small',
      'posture:low',
    ]);
  });
});
