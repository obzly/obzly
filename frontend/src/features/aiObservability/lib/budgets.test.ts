import dayjs from 'dayjs';
import timezone from 'dayjs/plugin/timezone';
import utc from 'dayjs/plugin/utc';
import { describe, expect, it } from 'vitest';

import type { AiBudget, AiBudgetStatus } from '../../../entity/aiUsage';
import { budgetState, forecastLabel, progressPercent, scopeLabel, spentLabel } from './budgets';

// main.tsx extends these for the app.
dayjs.extend(utc);
dayjs.extend(timezone);

const budget = (overrides: Partial<AiBudget> = {}): AiBudget => ({
  id: 'b1',
  projectId: 'p1',
  name: 'Daily per engineer',
  amountUsd: 40,
  period: 'day',
  timeZone: 'UTC',
  scope: 'each_user',
  scopeValue: '',
  thresholds: [80, 100],
  channelIds: [],
  includeClaudeCode: true,
  includeLlmApps: false,
  createdAt: '',
  updatedAt: '',
  ...overrides,
});

const status = (overrides: Partial<AiBudgetStatus> = {}): AiBudgetStatus => ({
  budgetId: 'b1',
  periodStart: 0,
  periodEnd: Date.UTC(2026, 10, 1),
  spentUsd: 0,
  percent: 0,
  forecastUsd: 0,
  groups: 0,
  groupsWarning: 0,
  groupsOver: 0,
  top: [],
  ...overrides,
});

describe('budgetState', () => {
  it('is on track below the first threshold, a warning past it and over from 100%', () => {
    expect(budgetState(79.9, [80, 100])).toBe('ok');
    expect(budgetState(80, [80, 100])).toBe('warning');
    expect(budgetState(100, [80, 100])).toBe('over');
    expect(budgetState(60, [100, 50])).toBe('warning');
    expect(budgetState(99, [])).toBe('ok');
  });
});

describe('progressPercent', () => {
  it('clamps the bar to 0-100', () => {
    expect(progressPercent(110)).toBe(100);
    expect(progressPercent(-5)).toBe(0);
    expect(progressPercent(Number.NaN)).toBe(0);
    expect(progressPercent(42.5)).toBe(42.5);
  });
});

describe('labels', () => {
  it('describes the scope and what counts', () => {
    expect(scopeLabel(budget({ scope: 'project', includeLlmApps: true }))).toBe(
      'Whole project · Claude Code + LLM apps',
    );
    expect(scopeLabel(budget({ scope: 'model', scopeValue: 'claude-opus-4-1' }))).toBe(
      'One model: claude-opus-4-1 · Claude Code',
    );
  });

  it('counts users over for per-user budgets', () => {
    const s = status({ groups: 42, groupsWarning: 2, groupsOver: 1 });
    expect(spentLabel(budget(), s)).toBe('1 of 42 users over');
    expect(forecastLabel(budget(), s)).toBe('3 users past 80%, 1 over');
  });

  it('shows the forecast for a single budget', () => {
    const s = status({ spentUsd: 8412, percent: 70, forecastUsd: 11630 });
    expect(spentLabel(budget({ scope: 'project' }), s)).toBe('$8,412.00');
    expect(forecastLabel(budget({ scope: 'project' }), s)).toBe('Forecast $11,630.00 by 31 Oct');
    expect(
      forecastLabel(budget({ scope: 'project', timeZone: 'Asia/Kolkata' }), {
        ...s,
        periodEnd: Date.UTC(2026, 9, 31, 18, 30),
      }),
    ).toBe('Forecast $11,630.00 by 31 Oct');
    expect(forecastLabel(budget({ scope: 'project' }), status({ percent: 110 }))).toBe(
      'Over budget this period',
    );
  });
});
