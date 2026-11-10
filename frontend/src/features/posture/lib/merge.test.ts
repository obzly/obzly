import { describe, expect, it } from 'vitest';

import type { Finding, PostureSummary } from '../../../entity/posture';
import { fillRemediation, mergeFindings, mergeSummary, mergeTrends } from './merge';

const site = (siteId: string) => ({ siteId, siteName: siteId });

const summary = (
  passed: number,
  failed: number,
  source: 'native' | 'baseline',
): PostureSummary => ({
  standards: [
    {
      standard: 'aws-fsbp',
      name: 'AWS FSBP',
      passed,
      failed,
      evaluated: passed + failed,
      percent: 0,
      sources: [source],
      trend: [{ day: '2026-11-10', percent: (passed / (passed + failed)) * 100 }],
    },
  ],
  open: { high: failed },
  suppressed: 1,
  resolved30d: 2,
  coverage: { native: 1, fallback: 0, baseline: 0, notCovered: 1, stale: 0 },
  lastScanAt: '2026-11-10T10:00:00Z',
});

describe('merging posture across Sites', () => {
  it('adds controls up, recomputes the percent and marks mixed sources', () => {
    const merged = mergeSummary([
      { site: site('a'), value: summary(3, 1, 'native') },
      { site: site('b'), value: summary(1, 3, 'baseline') },
      { site: site('c'), error: 'unavailable' },
    ]);
    const fsbp = merged.standards[0];
    expect([fsbp.passed, fsbp.failed, fsbp.evaluated, fsbp.percent]).toEqual([4, 4, 8, 50]);
    expect(fsbp.sources).toEqual(['baseline', 'native']);
    expect(fsbp.trend).toEqual([{ day: '2026-11-10', percent: 50 }]);
    expect(merged.open).toEqual({ high: 4 });
    expect(merged.coverage.notCovered).toBe(2);
  });

  it('orders findings most severe first and adds totals', () => {
    const finding = (id: string, severity: Finding['severity'], lastSeen: string) =>
      ({ id, severity, lastSeen }) as Finding;
    const merged = mergeFindings([
      { site: site('a'), value: { total: 10, findings: [finding('1', 'medium', '2026-11-10')] } },
      {
        site: site('b'),
        value: {
          total: 5,
          findings: [finding('2', 'critical', '2026-11-01'), finding('3', 'medium', '2026-11-11')],
        },
      },
    ]);
    expect(merged.total).toBe(15);
    expect(merged.findings.map((item) => [item.id, item.siteId])).toEqual([
      ['2', 'b'],
      ['3', 'b'],
      ['1', 'a'],
    ]);
  });

  it('adds trend counts and averages scores', () => {
    const period = (open: number, score: number) => ({
      periods: [
        {
          key: '2026-11',
          from: '2026-11-01',
          to: '2026-11-10',
          open: { high: open },
          opened: 1,
          resolved: 1,
          scores: { 'aws-fsbp': score },
        },
      ],
    });
    const merged = mergeTrends([
      { site: site('a'), value: period(2, 80) },
      { site: site('b'), value: period(3, 60) },
    ]);
    expect(merged.periods[0]).toMatchObject({
      open: { high: 5 },
      opened: 2,
      resolved: 2,
      scores: { 'aws-fsbp': 70 },
    });
  });

  it('fills remediation placeholders from the finding', () => {
    const finding = {
      resourceKey: 'arn:aws:ec2:eu-west-1:111122223333:security-group/sg-0abc',
      region: 'eu-west-1',
      evidence: { name: 'sg-0abc' },
    } as unknown as Finding;
    expect(fillRemediation('--region {region} --group-id {name} -g {resourceGroup}', finding)).toBe(
      '--region eu-west-1 --group-id sg-0abc -g <resource group>',
    );
  });
});
