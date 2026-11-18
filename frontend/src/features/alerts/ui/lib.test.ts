import { describe, expect, it } from 'vitest';

import { managedRuleInfo, ruleSqlSummary } from './lib';

describe('managedRuleInfo', () => {
  it('names the feature that keeps each managed rule', () => {
    expect(managedRuleInfo({ managedBy: 'ai-budget:1' })?.label).toBe('Budget');
    expect(managedRuleInfo({ managedBy: 'cloud-cost:budget:1' })?.label).toBe('Cost');
    expect(managedRuleInfo({ managedBy: 'cloud-cost:anomalies:1' })?.label).toBe('Cost');
    expect(managedRuleInfo({ managedBy: 'cloud-posture:findings:1' })?.label).toBe('Posture');
    expect(managedRuleInfo({ managedBy: 'synthetics:check:1' })?.label).toBe('Synthetics');
  });

  it('is undefined for a rule nobody manages', () => {
    expect(managedRuleInfo({ managedBy: '' })).toBeUndefined();
    expect(managedRuleInfo({})).toBeUndefined();
  });
});

describe('ruleSqlSummary', () => {
  it('puts the SQL on one line and shortens it', () => {
    expect(ruleSqlSummary('SELECT count(*) AS value\n  FROM logs')).toBe(
      'SELECT count(*) AS value FROM logs',
    );
    const summary = ruleSqlSummary(`SELECT '${'x'.repeat(100)}'`);
    expect(summary).toHaveLength(60);
    expect(summary.endsWith('…')).toBe(true);
  });
});
