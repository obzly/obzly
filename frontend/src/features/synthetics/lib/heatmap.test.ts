import { describe, expect, it } from 'vitest';

import { heatmapGrid, hourLabel, hourWindow } from './heatmap';

const HOUR = 3_600_000;

describe('hourWindow', () => {
  it('ends at the end of the current local clock hour', () => {
    const now = new Date(2026, 10, 3, 14, 37, 12).getTime();
    const window = hourWindow(86400, now);
    expect(window.to).toBe(new Date(2026, 10, 3, 15, 0, 0).getTime());
    expect(window.to - window.from).toBe(24 * HOUR);
    expect(new Date(window.from).getMinutes()).toBe(0);
  });
});

describe('heatmapGrid', () => {
  it('labels each point by the hour it covers and drops the hour before the range', () => {
    const from = new Date(2026, 10, 3, 12, 0, 0).getTime();
    const point = (hours: number, value: number) => ({
      timestamp: new Date(from + hours * HOUR).toISOString(),
      value,
    });
    const grid = heatmapGrid(
      [
        { labels: { location: 'b' }, points: [point(0, 0), point(1, 1), point(2, 0.5)] },
        { labels: { location: 'a' }, points: [point(1, 0.975)] },
      ],
      from,
    );
    expect(grid.names).toEqual(['a', 'b']);
    expect(grid.hours).toEqual([from, from + HOUR]);
    expect(grid.cells).toEqual([
      [0, 1, 100],
      [1, 1, 50],
      [0, 0, 97.5],
    ]);
  });
});

describe('hourLabel', () => {
  it('adds the date for a week', () => {
    const hour = new Date(2026, 10, 3, 14, 0, 0).getTime();
    expect(hourLabel(hour, true).length).toBeGreaterThan(hourLabel(hour, false).length);
    expect(hourLabel(hour, true)).toContain('3');
  });
});
