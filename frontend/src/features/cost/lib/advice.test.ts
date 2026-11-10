import { describe, expect, it } from 'vitest';

import type { CostRecommendation, Plan, PurchaseOption } from '../../../entity/cost';
import {
  defaultOption,
  howToBuy,
  isActive,
  mergeRecommendations,
  paybackText,
  totalsByCurrency,
} from './advice';

const rec = (overrides: Partial<CostRecommendation>): CostRecommendation => ({
  key: 'k',
  projectId: 'p',
  connectionName: 'Contoso-Payer',
  provider: 'aws',
  source: 'compute_optimizer',
  kind: 'rightsizing',
  account: '111122223333',
  region: 'eu-west-1',
  resourceKey: 'arn:aws:ec2:eu-west-1:111122223333:instance/i-0a1',
  resourceName: 'web-1',
  resourceType: 'aws_ec2_instance',
  action: 'Change m5.2xlarge to m5.xlarge',
  currency: 'USD',
  monthlySavings: 100,
  savingsBasis: 'provider',
  effort: 'low',
  risk: 'low',
  evidence: {},
  status: 'open',
  ...overrides,
});

const option = (overrides: Partial<PurchaseOption>): PurchaseOption => ({
  term: '1y',
  payment: 'no_upfront',
  offered: true,
  hourlyCommitment: 2,
  upfront: 0,
  monthlyRecurring: 1460,
  monthlySavings: 500,
  savingsPercent: 25,
  breakEvenMonths: 0,
  averageUtilization: 97,
  coveragePercent: 0,
  lines: [],
  labels: [],
  ...overrides,
});

describe('cost advice', () => {
  it('adds savings per currency and keeps unknown ones apart, never as zero', () => {
    const { totals, unknown } = totalsByCurrency(
      [
        rec({ monthlySavings: 100 }),
        rec({ monthlySavings: 40.5 }),
        rec({ monthlySavings: 90, currency: 'EUR' }),
        rec({ monthlySavings: null, currency: '' }),
      ],
      (item) => item.monthlySavings,
      (item) => item.currency,
    );
    expect(totals).toEqual([
      { currency: 'USD', amount: 140.5, count: 2 },
      { currency: 'EUR', amount: 90, count: 1 },
    ]);
    expect(unknown).toBe(1);
  });

  it('merges Sites, labelling each item with its Site, and skips Sites that failed', () => {
    const site = { siteId: 'eu', siteName: 'Europe' };
    const merged = mergeRecommendations([
      { site, value: { items: [rec({})], sections: [] } },
      { site: { siteId: 'us', siteName: 'US' }, error: 'unreachable' },
    ]);
    expect(merged.items).toHaveLength(1);
    expect(merged.items[0].siteName).toBe('Europe');
  });

  it('treats open and planned as to do', () => {
    expect(isActive(rec({ status: 'planned' }))).toBe(true);
    expect(isActive(rec({ status: 'dismissed' }))).toBe(false);
  });

  it('starts the planner on the lowest-risk option and says when it pays back', () => {
    const plan = {
      type: 'compute_sp',
      connectionName: 'Contoso-Payer',
      options: [
        option({ term: '3y', payment: 'all_upfront', labels: ['most_savings'] }),
        option({ labels: ['lowest_risk'] }),
      ],
    } as unknown as Plan;
    expect(defaultOption(plan)?.term).toBe('1y');
    expect(paybackText(option({}))).toBe('saves from month 1');
    expect(paybackText(option({ upfront: 6000, breakEvenMonths: 3.2 }))).toBe(
      'pays back in month 4',
    );
    expect(howToBuy(plan, option({ commitment: '2.00 an hour' })).join(' ')).toContain('2.00 USD');
  });
});
