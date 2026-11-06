import dayjs from 'dayjs';
import timezone from 'dayjs/plugin/timezone';
import utc from 'dayjs/plugin/utc';
import { describe, expect, it } from 'vitest';

import { UTC_TIMEZONE, timezoneShiftMs, unshiftTimezone } from './timezone';

dayjs.extend(utc);
dayjs.extend(timezone);

const asUTC = (ms: number) => new Date(ms).toISOString().slice(11, 16);

describe('timezoneShiftMs', () => {
  it('makes UTC rendering show the chosen zone, including half-hour offsets', () => {
    const instant = Date.parse('2026-10-27T10:00:00Z');
    expect(asUTC(instant + timezoneShiftMs(instant, 'Asia/Kolkata'))).toBe('15:30');
    expect(asUTC(instant + timezoneShiftMs(instant, UTC_TIMEZONE))).toBe('10:00');
  });

  it('follows daylight saving changes within a range', () => {
    const summer = Date.parse('2026-10-24T12:00:00Z');
    const winter = Date.parse('2026-10-26T12:00:00Z');
    expect(asUTC(summer + timezoneShiftMs(summer, 'Europe/Berlin'))).toBe('14:00');
    expect(asUTC(winter + timezoneShiftMs(winter, 'Europe/Berlin'))).toBe('13:00');
  });

  it('round-trips through unshiftTimezone', () => {
    // Either side of Berlin's clock change; the repeated hour itself is ambiguous.
    const instants = ['2026-10-24T23:30:00Z', '2026-10-25T02:00:00Z', '2026-03-29T01:30:00Z'];
    for (const zone of ['Asia/Kolkata', 'America/New_York', 'Europe/Berlin', UTC_TIMEZONE]) {
      for (const iso of instants) {
        const instant = Date.parse(iso);
        expect(unshiftTimezone(instant + timezoneShiftMs(instant, zone), zone)).toBe(instant);
      }
    }
  });
});
