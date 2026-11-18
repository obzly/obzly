import { describe, expect, it } from 'vitest';

import type { RuleActivityResponse } from '../../../entity/alerting';
import { noisiestRules, ruleDays, summaryLine } from './activity';

const activity: RuleActivityResponse = {
  days: [
    '2026-11-12',
    '2026-11-13',
    '2026-11-14',
    '2026-11-15',
    '2026-11-16',
    '2026-11-17',
    '2026-11-18',
  ],
  trackedSince: '2026-11-14T09:30:00Z',
  rules: [
    {
      ruleId: 'a',
      fired: [0, 0, 1, 0, 2, 0, 3],
      notifications: [0, 0, 2, 0, 4, 0, 9],
      firedTotal: 6,
      notificationsTotal: 15,
    },
    {
      ruleId: 'b',
      fired: [0, 0, 0, 0, 0, 0, 0],
      notifications: [0, 0, 0, 0, 0, 0, 0],
      firedTotal: 0,
      notificationsTotal: 0,
    },
    {
      ruleId: 'c',
      fired: [0, 0, 0, 0, 0, 0, 1],
      notifications: [0, 0, 0, 0, 0, 0, 15],
      firedTotal: 1,
      notificationsTotal: 15,
    },
  ],
};

describe('ruleDays', () => {
  it('leaves days before counting started unknown, and counts the start day', () => {
    const days = ruleDays(activity, { id: 'a', createdAt: '2026-10-01T00:00:00Z' });
    expect(days.map((d) => d.fired)).toEqual([null, null, 1, 0, 2, 0, 3]);
    expect(days[6]).toEqual({ day: '2026-11-18', fired: 3, notifications: 9 });
  });

  it('leaves days before the rule was created unknown', () => {
    const days = ruleDays(activity, { id: 'b', createdAt: '2026-11-17T23:00:00Z' });
    expect(days.map((d) => d.notifications)).toEqual([null, null, null, null, null, 0, 0]);
  });

  it('is all unknown for a rule the response lacks', () => {
    expect(ruleDays(activity, { id: 'new' }).every((d) => d.fired === null)).toBe(true);
  });

  it('knows every day when neither date is set', () => {
    const days = ruleDays({ ...activity, trackedSince: null }, { id: 'a' });
    expect(days.map((d) => d.fired)).toEqual([0, 0, 1, 0, 2, 0, 3]);
  });
});

describe('noisiestRules', () => {
  it('ranks by notifications, then firings, and skips silent and unknown rules', () => {
    const rows = noisiestRules(activity, [
      { id: 'a', name: 'Pod restarts' },
      { id: 'b', name: 'Quiet' },
      { id: 'c', name: 'Disk full' },
    ]);
    expect(rows).toEqual([
      { ruleId: 'a', name: 'Pod restarts', notifications: 15, fired: 6, perFiring: 2.5 },
      { ruleId: 'c', name: 'Disk full', notifications: 15, fired: 1, perFiring: 15 },
    ]);
    expect(noisiestRules(activity, [{ id: 'a', name: 'Only' }], 1)).toHaveLength(1);
  });
});

describe('summaryLine', () => {
  it('says firing, rules and silences, and pending only when there are some', () => {
    expect(summaryLine({ firing: 3, pending: 0, rules: 7, silences: 1 })).toBe(
      '3 firing · 7 rules · 1 silence',
    );
    expect(summaryLine({ firing: 0, pending: 2, rules: 1, silences: 0 })).toBe(
      '0 firing · 2 pending · 1 rule · 0 silences',
    );
  });
});
