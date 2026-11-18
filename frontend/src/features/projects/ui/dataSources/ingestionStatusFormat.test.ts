import { describe, expect, it } from 'vitest';

import type { IngestionSource } from '../../../../entity/projects';
import { dataSourceCatalog } from './catalog';
import { formatAgo, formatRate, locationText, signalChip } from './ingestionStatusFormat';

// Every data source id the backend can report (internal/features/ingestion/identity.go).
const BACKEND_DATA_SOURCE_IDS = [
  'nginx', 'apache', 'postgresql', 'mysql', 'redis', 'valkey', 'mongodb', 'rabbitmq', 'kafka',
  'elasticsearch', 'haproxy', 'memcached', 'zookeeper', 'couchdb', 'riak', 'sqlserver', 'iis',
  'vcenter', 'cassandra', 'consul', 'etcd', 'envoy', 'traefik', 'flink', 'spark', 'claude-code',
  'snmp', 'http-check', 'jvm', 'statsd', 'syslog', 'prometheus', 'docker', 'kubernetes', 'linux',
  'windows', 'llm-app-otel', 'lang-java', 'lang-python', 'lang-nodejs', 'lang-go', 'lang-dotnet',
  'lang-ruby', 'lang-php', 'lang-rust', 'lang-cpp', 'lang-erlang', 'lang-swift', 'lang-browser-js',
]; // prettier-ignore

const source = (overrides: Partial<IngestionSource>): IngestionSource => ({
  id: 'abc',
  name: 'checkout',
  identifiedBy: 'k8s_workload',
  location: {},
  health: 'sending',
  firstSeen: '2026-11-18T10:00:00Z',
  lastSeen: '2026-11-18T10:00:00Z',
  signals: [],
  ...overrides,
});

describe('ingestion status formatting', () => {
  it('knows every data source the backend detects', () => {
    const ids = new Set(dataSourceCatalog.map((d) => d.id));
    expect(BACKEND_DATA_SOURCE_IDS.filter((id) => !ids.has(id))).toEqual([]);
  });

  it('describes where a source runs without repeating its name', () => {
    expect(
      locationText(
        source({
          location: {
            cluster: 'prd',
            namespace: 'shop',
            workload: 'checkout',
            serviceName: 'checkout',
          },
        }),
      ),
    ).toBe('prd / shop / checkout');
    expect(
      locationText(
        source({
          name: 'billing',
          identifiedBy: 'service',
          location: { serviceName: 'billing', serviceNamespace: 'payments', host: 'vm-7' },
        }),
      ),
    ).toBe('namespace payments · vm-7');
  });

  it('shows the rate only for a signal that is sending', () => {
    const base = { firstSeen: '', lastSeen: '', recentCount: 300, perMinute: 2400 };
    expect(signalChip({ ...base, signal: 'logs', health: 'sending' })).toBe('Logs 2.4k/min');
    expect(signalChip({ ...base, signal: 'traces', health: 'quiet' })).toBe('Traces · quiet');
    expect(formatRate(4.5)).toBe('4.5/min');
    expect(formatRate(52_300)).toBe('52k/min');
  });

  it('says how long ago data last arrived', () => {
    const now = Date.parse('2026-11-18T12:00:00Z');
    expect(formatAgo('2026-11-18T11:59:56Z', now)).toBe('4 s ago');
    expect(formatAgo('2026-11-18T11:00:00Z', now)).toBe('1 h ago');
    expect(formatAgo('2026-11-16T12:00:00Z', now)).toBe('2 days ago');
  });
});
