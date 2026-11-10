import { describe, expect, it } from 'vitest';

import { costDraftFrom, costDraftReady, costSettingsFrom, estimateCost } from './costChoice';

describe('cost choices', () => {
  it('estimates Cost Explorer calls per month and keeps Azure free', () => {
    expect(estimateCost('aws', 1, 12)).toEqual({
      apiMonthly: 1.2,
      apiRequests: 120,
      backfillRequests: 2,
    });
    expect(estimateCost('aws', 20, 24).apiMonthly).toBeCloseTo(6.3);
    expect(estimateCost('azure', 20, 6).apiMonthly).toBe(0);
  });

  it('needs the export location only for export only, and drops it for the API', () => {
    const draft = { ...costDraftFrom(), source: 'export' as const };
    expect(costDraftReady(draft, 'aws')).toBe(false);
    expect(costDraftReady({ ...draft, bucket: 'contoso-billing' }, 'aws')).toBe(true);
    expect(costDraftReady({ ...draft, storageAccount: 'contosobilling' }, 'azure')).toBe(false);
    expect(costSettingsFrom({ ...draft, source: 'api' }, 'aws')).toEqual({
      source: 'api',
      scope: 'account',
      refreshHours: 12,
    });
    expect(costSettingsFrom({ ...draft, scope: 'via', viaConnectionId: 'payer' }, 'azure')).toEqual(
      { source: 'auto', scope: 'via', viaConnectionId: 'payer' },
    );
  });
});
