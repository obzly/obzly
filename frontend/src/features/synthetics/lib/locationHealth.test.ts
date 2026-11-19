import { describe, expect, it } from 'vitest';

import { type MonitorLocations, failsSomewhere, summarizeLocations } from './locationHealth';

const monitor = (
  id: string,
  states: [string, 'up' | 'warn' | 'down'][],
  inMaintenance = false,
): MonitorLocations => ({
  id,
  inMaintenance,
  locations: states.map(([location, status]) => ({
    location,
    up: status !== 'down',
    warn: status === 'warn',
  })),
});

describe('summarizeLocations', () => {
  it('counts each monitor once per location it reports from', () => {
    const summary = summarizeLocations(
      ['eu-west', 'us-east'],
      [
        monitor('a', [
          ['eu-west', 'up'],
          ['us-east', 'down'],
        ]),
        monitor('b', [['eu-west', 'warn']]),
        monitor('c', [['us-east', 'down']], true),
      ],
    );
    expect(summary).toEqual([
      { location: 'eu-west', total: 2, counts: { up: 1, warn: 1, down: 0, maintenance: 0 } },
      { location: 'us-east', total: 2, counts: { up: 0, warn: 0, down: 1, maintenance: 1 } },
    ]);
  });

  it('keeps a location with no results at zero', () => {
    expect(summarizeLocations(['ap-south'], [monitor('a', [['eu-west', 'up']])])).toEqual([
      { location: 'ap-south', total: 0, counts: { up: 0, warn: 0, down: 0, maintenance: 0 } },
    ]);
  });
});

describe('failsSomewhere', () => {
  it('is true when any location fails or warns', () => {
    expect(
      failsSomewhere(
        monitor('a', [
          ['eu-west', 'up'],
          ['us-east', 'warn'],
        ]),
      ),
    ).toBe(true);
  });

  it('is false when every location passes', () => {
    expect(failsSomewhere(monitor('a', [['eu-west', 'up']]))).toBe(false);
  });

  it('ignores failures during maintenance', () => {
    expect(failsSomewhere(monitor('a', [['eu-west', 'down']], true))).toBe(false);
  });
});
