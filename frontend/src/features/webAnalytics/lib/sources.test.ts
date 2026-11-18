import { describe, expect, it } from 'vitest';

import type { WebAnalyticsSource } from '../../../entity/webAnalytics';
import { sourceGaps, sourceLabel } from './sources';

const source = (key: string, overrides: Partial<WebAnalyticsSource> = {}): WebAnalyticsSource => ({
  key,
  name: key,
  defaultName: key,
  identifiedBy: 'service',
  identity: [],
  kind: 'Nginx',
  format: 'combined',
  fields: {},
  where: '',
  hosts: [],
  requests: 1,
  share: 0.5,
  hasReferrer: true,
  hasUserAgent: true,
  hasDuration: true,
  hasTls: true,
  mergedFrom: [],
  firstSeen: '',
  lastSeen: '',
  ...overrides,
});

describe('sourceGaps', () => {
  it('names the fields a format lacks', () => {
    expect(sourceGaps(source('a'))).toEqual([]);
    expect(
      sourceGaps(source('b', { hasUserAgent: false, hasDuration: false, hasTls: false })),
    ).toEqual(['no referrer/browser', 'no response time', 'no TLS']);
  });
});

describe('sourceLabel', () => {
  const list = [source('nginx-ingress'), source('envoy-edge'), source('apache-legacy')];
  it('says how many sources the report is built from', () => {
    expect(sourceLabel(list, [])).toBe('All 3 sources');
    expect(sourceLabel(list, ['envoy-edge'])).toBe('envoy-edge');
    expect(sourceLabel(list, ['envoy-edge', 'apache-legacy'])).toBe('2 of 3 sources');
    expect(sourceLabel([], [])).toBe('No sources yet');
  });
});
