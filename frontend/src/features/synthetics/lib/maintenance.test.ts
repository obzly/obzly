import { describe, expect, it } from 'vitest';

import type { MaintenanceOccurrence, PromQLPoint } from '../../../entity/synthetics';
import {
  activeFor,
  calendarDays,
  coveredMs,
  dayStart,
  maintenanceRuns,
  markMaintenance,
  occurrencesByMonitor,
  occurrencesKey,
  relevantOccurrence,
  scheduleText,
  upcomingFor,
  uptimeExcluding,
  withMaintenance,
} from './maintenance';
import { STATUS_ORDER } from './status';

const t0 = Date.parse('2026-10-27T00:00:00Z');
const minutes = (n: number) => t0 + n * 60_000;
const iso = (at: number) => new Date(at).toISOString();

const occurrence = (
  start: number,
  end: number,
  monitorIds = ['c1'],
  windowId = 'w1',
): MaintenanceOccurrence => ({
  windowId,
  name: 'Weekly DB patching',
  reason: '',
  start: iso(start),
  end: iso(end),
  monitorIds,
});

describe('withMaintenance', () => {
  it('Test_StatusInMaintenance_RedAndAmberBecomeMaintenance_GreenStaysGreen', () => {
    expect(withMaintenance('down', true)).toBe('maintenance');
    expect(withMaintenance('warn', true)).toBe('maintenance');
    expect(withMaintenance('impacted', true)).toBe('maintenance');
    expect(withMaintenance('up', true)).toBe('up');
    expect(withMaintenance('off', true)).toBe('off');
    expect(withMaintenance('none', true)).toBe('none');
    expect(withMaintenance('down', false)).toBe('down');
  });

  it('Test_StatusOrder_MaintenanceSortsAfterProblemsBeforeGreen', () => {
    expect(STATUS_ORDER.maintenance).toBeGreaterThan(STATUS_ORDER.warn);
    expect(STATUS_ORDER.maintenance).toBeLessThan(STATUS_ORDER.up);
  });
});

describe('activeFor / upcomingFor', () => {
  const list = [occurrence(minutes(60), minutes(120)), occurrence(minutes(0), minutes(30), ['c2'])];

  it('Test_ActiveFor_OnlyInsideTheOccurrenceAndOnlyItsMonitors', () => {
    expect(activeFor('c1', list, minutes(90))?.windowId).toBe('w1');
    expect(activeFor('c1', list, minutes(120))).toBeUndefined();
    expect(activeFor('c1', list, minutes(10))).toBeUndefined();
    expect(activeFor('c2', list, minutes(10))).toBeDefined();
  });

  it('Test_UpcomingFor_WithinTheHour', () => {
    expect(upcomingFor('c1', list, minutes(5))).toBeDefined();
    expect(upcomingFor('c1', list, minutes(-5))).toBeUndefined();
  });
});

describe('uptimeExcluding', () => {
  const step = 60;
  // Ten one-minute steps ending at minutes 1..10; one run each, failing in 4..6.
  const runs: PromQLPoint[] = Array.from({ length: 10 }, (_, i) => ({
    timestamp: iso(minutes(i + 1)),
    value: 1,
  }));
  const passed: PromQLPoint[] = runs.map((point, i) => ({
    timestamp: point.timestamp,
    value: i >= 3 && i <= 5 ? 0 : 1,
  }));

  it('Test_UptimeExcluding_LeavesTheMaintenanceStepsOut', () => {
    expect(uptimeExcluding(passed, runs, step, [])).toBeCloseTo(0.7);
    expect(uptimeExcluding(passed, runs, step, [{ start: minutes(3), end: minutes(6) }])).toBe(1);
  });

  it('Test_UptimeExcluding_AllInMaintenance_IsUndefined', () => {
    expect(
      uptimeExcluding(passed, runs, step, [{ start: minutes(0), end: minutes(10) }]),
    ).toBeUndefined();
  });
});

describe('markMaintenance', () => {
  it('Test_MarkMaintenance_HatchesSlotsWhoseMiddleIsInside_OrThatHoldAShortOne', () => {
    const slots = markMaintenance(['up', 'down', 'down', 'up', 'none', 'up'], t0, 30 * 60, [
      { start: minutes(20), end: minutes(80) }, // middles at 15, 45, 75: slots 1 and 2
      { start: minutes(245), end: minutes(255) }, // past the strip
      { start: minutes(125), end: minutes(130) }, // a short one inside slot 4
    ]);
    expect(slots).toEqual(['up', 'maintenance', 'maintenance', 'up', 'maintenance', 'up']);
  });

  it('Test_MaintenanceRuns_GroupsConsecutiveSlots', () => {
    expect(maintenanceRuns(['up', 'maintenance', 'maintenance', 'up', 'maintenance'])).toEqual([
      { start: 1, length: 2 },
      { start: 4, length: 1 },
    ]);
  });
});

describe('coveredMs', () => {
  it('Test_CoveredMs_ClipsToTheRangeAndCountsOverlapsOnce', () => {
    expect(
      coveredMs(
        [
          { start: minutes(-30), end: minutes(30) },
          { start: minutes(20), end: minutes(40) },
          { start: minutes(100), end: minutes(200) },
        ],
        minutes(0),
        minutes(120),
      ),
    ).toBe(60 * 60_000);
  });
});

describe('relevantOccurrence', () => {
  it('Test_RelevantOccurrence_ActiveThenSoonThenLatestPast', () => {
    const past = occurrence(minutes(0), minutes(10), ['c1'], 'past');
    const soon = occurrence(minutes(130), minutes(140), ['c1'], 'soon');
    const active = occurrence(minutes(100), minutes(150), ['c1'], 'active');
    expect(
      relevantOccurrence('c1', [past, soon, active], minutes(110), minutes(-60))?.windowId,
    ).toBe('active');
    expect(relevantOccurrence('c1', [past, soon], minutes(110), minutes(-60))?.windowId).toBe(
      'soon',
    );
    expect(relevantOccurrence('c1', [past], minutes(110), minutes(-60))?.windowId).toBe('past');
    expect(relevantOccurrence('c1', [past], minutes(110), minutes(20))).toBeUndefined();
  });
});

describe('calendarDays', () => {
  it('Test_CalendarDays_MarksEachLocalDayAnOccurrenceTouches', () => {
    const from = dayStart(Date.parse('2026-10-27T12:00:00'));
    const overnight = occurrence(
      Date.parse('2026-10-28T23:00:00'),
      Date.parse('2026-10-29T01:00:00'),
    );
    expect(calendarDays([overnight], from, 4).get('w1')).toEqual([false, true, true, false]);
  });
});

describe('scheduleText', () => {
  it('Test_ScheduleText_Recurring', () => {
    expect(
      scheduleText({
        kind: 'weekly',
        weekdays: ['tue'],
        startTime: '01:00',
        durationMinutes: 60,
        timeZone: 'UTC',
      }),
    ).toBe('Weekly · Tue 01:00 · 1 h · UTC');
    expect(
      scheduleText({
        kind: 'monthly_weekday',
        weekOfMonth: -1,
        weekday: 'sun',
        startTime: '03:00',
        durationMinutes: 90,
        timeZone: 'Asia/Kolkata',
      }),
    ).toBe('Monthly · last Sun 03:00 · 1 h 30 min · Asia/Kolkata');
  });
});

describe('occurrencesByMonitor', () => {
  it('Test_OccurrenceIndex_ListsEachMonitorsOccurrences', () => {
    const first = occurrence(minutes(0), minutes(60), ['c1', 'c2']);
    const second = occurrence(minutes(120), minutes(180), ['c2'], 'w2');
    const index = occurrencesByMonitor([first, second]);
    expect(index.get('c1')).toEqual([first]);
    expect(index.get('c2')).toEqual([first, second]);
    expect(index.get('c3')).toBeUndefined();
  });

  it('Test_OccurrencesKey_ChangesWithScopeOrTimes', () => {
    const base = [occurrence(minutes(0), minutes(60))];
    expect(occurrencesKey([occurrence(minutes(0), minutes(60))])).toBe(occurrencesKey(base));
    expect(occurrencesKey([occurrence(minutes(0), minutes(90))])).not.toBe(occurrencesKey(base));
    expect(occurrencesKey([occurrence(minutes(0), minutes(60), ['c1', 'c2'])])).not.toBe(
      occurrencesKey(base),
    );
  });
});
