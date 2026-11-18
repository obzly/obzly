import { describe, expect, it } from 'vitest';

import { type LocalHour, buildHeatmap } from './heatmap';

const utc = (date: Date): LocalHour => ({
  day: date.toISOString().slice(0, 10),
  weekday: ['Sun', 'Mon', 'Tue', 'Wed', 'Thu', 'Fri', 'Sat'][date.getUTCDay()],
  hour: date.getUTCHours(),
});

describe('buildHeatmap', () => {
  const now = new Date(Date.UTC(2026, 10, 18, 14, 32)); // a Wednesday

  it('lays the last 7 days out by weekday and hour', () => {
    const map = buildHeatmap(
      [
        { hour: '2026-11-18T14:00:00Z', requests: 30 },
        { hour: '2026-11-18T03:00:00Z', requests: 1 },
        { hour: '2026-11-12T19:00:00Z', requests: 50 },
        { hour: '2026-11-01T19:00:00Z', requests: 999 }, // outside the 7 days
      ],
      now,
      utc,
    );
    expect(map.rows.map((r) => r.label)).toEqual(['Thu', 'Fri', 'Sat', 'Sun', 'Mon', 'Tue', 'Wed']);
    expect(map.rows[6].cells[14]).toBe(30);
    expect(map.rows[0].cells[19]).toBe(50);
    expect(map.max).toBe(50);
    expect(map.busiest).toEqual({ label: 'Thu', hour: 19 });
    expect(map.quietestHour).toBe(0);
  });

  it('has no busiest or quietest hour without requests', () => {
    const map = buildHeatmap([], now, utc);
    expect(map.busiest).toBeNull();
    expect(map.quietestHour).toBeNull();
  });
});
