import { describe, expect, it } from 'vitest';

import type { CheckView, PromQLSeries } from '../../../entity/synthetics';
import { type MonitorStatus, downThreshold, impactedBy, monitorState } from './status';

const check = (overrides: Partial<CheckView> = {}): CheckView =>
  ({
    id: 'c1',
    enabled: true,
    downLocations: 0,
    locations: [],
    ...overrides,
  }) as CheckView;

const series = (location: string, value: number, checkId = 'c1'): PromQLSeries => ({
  labels: { check_id: checkId, location },
  points: [{ timestamp: '2026-10-25T00:00:00Z', value }],
});

describe('monitorState', () => {
  it('is blue when disabled, grey with no results', () => {
    expect(monitorState(check({ enabled: false }), [series('a', 1)], []).status).toBe('off');
    expect(monitorState(check(), [], []).status).toBe('none');
  });

  it('is green when every location passed', () => {
    expect(monitorState(check(), [series('a', 1), series('b', 1)], []).status).toBe('up');
  });

  it('is amber below the down threshold or on a warning, red at it', () => {
    const four = [series('a', 1), series('b', 1), series('c', 1), series('d', 0)];
    expect(monitorState(check(), four, []).status).toBe('warn');
    expect(monitorState(check({ downLocations: 1 }), four, []).status).toBe('down');
    expect(monitorState(check(), [series('a', 1)], [series('a', 1)]).status).toBe('warn');
    const twoDown = [series('a', 0), series('b', 0), series('c', 1), series('d', 1)];
    expect(monitorState(check(), twoDown, []).status).toBe('down');
  });

  it('counts a location as failed when any of its split series failed, whatever the order', () => {
    // Older probes wrote a timeout and the passing runs to different series
    // of one location (tls_verified only on runs that reached TLS).
    const split = (first: number, second: number) =>
      monitorState(check({ downLocations: 1 }), [series('a', first), series('a', second)], []);
    expect(split(1, 0).status).toBe('down');
    expect(split(0, 1).status).toBe('down');
    expect(split(1, 1).locations).toEqual([{ location: 'a', up: true, warn: false }]);
    const warned = monitorState(check(), [series('a', 1)], [series('a', 1), series('a', 0)]);
    expect(warned.status).toBe('warn');
  });

  it('ignores other monitors and locations it no longer runs in', () => {
    const state = monitorState(
      check({ locations: ['a'] }),
      [series('a', 1), series('b', 0), series('a', 0, 'other')],
      [],
    );
    expect(state.status).toBe('up');
    expect(state.locations.map((location) => location.location)).toEqual(['a']);
  });
});

describe('downThreshold', () => {
  it('defaults to half the locations, at least one', () => {
    expect(downThreshold(check(), 1)).toBe(1);
    expect(downThreshold(check(), 4)).toBe(2);
    expect(downThreshold(check(), 3)).toBe(2);
    expect(downThreshold(check({ downLocations: 3 }), 4)).toBe(3);
  });
});

describe('impactedBy', () => {
  it('marks failing children of a down parent, down a chain, but not healthy ones', () => {
    const gateway = check({ id: 'gateway' });
    const api = check({ id: 'api', parentIds: ['gateway'] });
    const checkout = check({ id: 'checkout', parentIds: ['api'] });
    const docs = check({ id: 'docs', parentIds: ['gateway'] });
    const status: Record<string, MonitorStatus> = {
      gateway: 'down',
      api: 'down',
      checkout: 'warn',
      docs: 'up',
    };
    const impacted = impactedBy([gateway, api, checkout, docs], (item) => status[item.id]);
    expect([...impacted.keys()].sort()).toEqual(['api', 'checkout']);
    expect(impacted.get('api')?.map((parent) => parent.id)).toEqual(['gateway']);
  });
});
