import { describe, expect, it } from 'vitest';

import type { WebAnalyticsSource } from '../../../entity/webAnalytics';
import { logsPresetFor, logsQueryFor, quoteValue, targetClause } from './logsQuery';

const source = (key: string, identity: [string, string][]): WebAnalyticsSource => ({
  key,
  name: key,
  defaultName: key,
  identifiedBy: 'service',
  identity: identity.map(([name, value]) => ({ name, value })),
  kind: 'Nginx',
  format: 'combined',
  fields: {},
  where: '',
  hosts: [],
  requests: 0,
  share: 0,
  hasReferrer: true,
  hasUserAgent: true,
  hasDuration: false,
  hasTls: false,
  mergedFrom: [],
  firstSeen: '',
  lastSeen: '',
});

describe('targetClause', () => {
  it('matches a status under every server name and in raw lines', () => {
    expect(targetClause({ kind: 'equals', field: 'status', value: '404' })).toBe(
      '(http_response_status_code=404 OR status=404 OR response_code=404 OR DownstreamStatus=404 OR sc_status=404 OR "\\" 404 ")',
    );
  });

  it('matches grouped paths by their fixed start', () => {
    const clause = targetClause({ kind: 'equals', field: 'path', value: '/catalog/:id' });
    expect(clause).toContain('url_path:/catalog/');
    expect(clause).toContain('" /catalog/"');
  });

  it('compares failed requests numerically', () => {
    expect(targetClause({ kind: 'atLeast', field: 'status', value: 400 })).toContain('status>=400');
  });
});

describe('logsQueryFor', () => {
  const nginx = source('a', [
    ['service_name', 'nginx'],
    ['host_name', 'web 1'],
  ]);
  const edge = source('b', [['obzly_source', 'envoy-edge']]);

  it('leaves the scope out when every source is selected', () => {
    expect(logsQueryFor({ kind: 'all' }, [nginx, edge], true)).not.toContain('service_name');
  });

  it('scopes to the selected sources, quoting values', () => {
    expect(
      logsQueryFor({ kind: 'equals', field: 'method', value: 'POST' }, [nginx, edge], false),
    ).toBe(
      '((service_name=nginx AND host_name="web 1") OR obzly_source=envoy-edge) AND ' +
        '(http_request_method=POST OR method=POST OR RequestMethod=POST OR cs_method=POST OR request_method=POST)',
    );
    expect(logsQueryFor({ kind: 'all' }, [edge], false)).toMatch(/^obzly_source=envoy-edge AND \(/);
  });

  it('quotes operators and escapes quotes', () => {
    expect(quoteValue('a"b')).toBe('"a\\"b"');
    expect(quoteValue('plain')).toBe('plain');
  });
});

describe('logsPresetFor', () => {
  it('picks the nearest Logs preset, capped at 72 h', () => {
    expect(logsPresetFor(15 * 60_000)).toBe('15m');
    expect(logsPresetFor(86_400_000)).toBe('24h');
    expect(logsPresetFor(7 * 86_400_000)).toBe('72h');
  });
});
