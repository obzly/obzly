import type {
  AiBudget,
  AiBudgetRequest,
  AiBudgetStatus,
  BudgetPeriod,
  BudgetScope,
} from '../../../entity/aiUsage';
import { formatInTimezone } from '../../../shared/time';
import { formatUsd } from './format';

export type BudgetState = 'ok' | 'warning' | 'over';

export const BUDGET_STATE_LABELS: Record<BudgetState, string> = {
  ok: 'On track',
  warning: 'Warning',
  over: 'Over budget',
};

// antd Tag/Progress colors per state: green, amber past the first
// threshold, red from 100%.
export const BUDGET_STATE_COLORS: Record<BudgetState, string> = {
  ok: '#16a34a',
  warning: '#d97706',
  over: '#dc2626',
};

export const SCOPE_OPTIONS: { value: BudgetScope; label: string; hint: string }[] = [
  { value: 'project', label: 'The whole project', hint: 'One total for everyone' },
  {
    value: 'each_user',
    label: 'Each user',
    hint: 'Every user gets this limit; alerts name the user',
  },
  { value: 'each_model', label: 'Each model', hint: 'e.g. keep Opus spend separate from Sonnet' },
  { value: 'user', label: 'One user', hint: 'Name the user (email for Claude Code)' },
  { value: 'model', label: 'One model', hint: 'Name the model' },
];

const PERIOD_LABELS: Record<BudgetPeriod, string> = {
  day: 'today',
  week: 'this week',
  month: 'this month',
};

export const budgetState = (percent: number, thresholds: number[]): BudgetState => {
  if (percent >= 100) return 'over';
  const first = thresholds.length ? Math.min(...thresholds) : 100;
  return percent >= first ? 'warning' : 'ok';
};

/** Width of the progress bar, 0-100. */
export const progressPercent = (percent: number): number =>
  Number.isFinite(percent) ? Math.min(100, Math.max(0, percent)) : 0;

export const periodLabel = (period: BudgetPeriod): string => PERIOD_LABELS[period];

export const isPerGroup = (scope: BudgetScope): boolean =>
  scope === 'each_user' || scope === 'each_model';

export const scopeLabel = (budget: AiBudget): string => {
  const scope =
    budget.scope === 'project'
      ? 'Whole project'
      : budget.scope === 'each_user'
        ? 'Each user'
        : budget.scope === 'each_model'
          ? 'Each model'
          : budget.scope === 'user'
            ? `One user: ${budget.scopeValue}`
            : `One model: ${budget.scopeValue}`;
  const counts = [
    budget.includeClaudeCode ? 'Claude Code' : null,
    budget.includeLlmApps ? 'LLM apps' : null,
  ].filter(Boolean);
  return `${scope} · ${counts.join(' + ')}`;
};

/** The big number on a card: spend, or for per-group budgets how many are over. */
export const spentLabel = (budget: AiBudget, status: AiBudgetStatus): string => {
  if (!isPerGroup(budget.scope)) return formatUsd(status.spentUsd);
  const noun = budget.scope === 'each_user' ? 'users' : 'models';
  return `${status.groupsOver} of ${status.groups} ${noun} over`;
};

export const forecastLabel = (budget: AiBudget, status: AiBudgetStatus): string => {
  if (isPerGroup(budget.scope)) {
    const noun = budget.scope === 'each_user' ? 'users' : 'models';
    if (status.groups === 0) return 'No spend yet this period';
    return `${status.groupsWarning + status.groupsOver} ${noun} past ${Math.min(...budget.thresholds)}%, ${status.groupsOver} over`;
  }
  if (status.percent >= 100) return 'Over budget this period';
  const lastMinute = status.periodEnd - 60_000;
  return `Forecast ${formatUsd(status.forecastUsd)} by ${formatInTimezone(lastMinute, 'D MMM', budget.timeZone)}`;
};

export const resetLabel = (status: AiBudgetStatus, timeZone: string): string =>
  `Resets ${formatInTimezone(status.periodEnd, 'ddd D MMM HH:mm', timeZone)} ${timeZone}`;

export const emptyBudgetRequest = (): AiBudgetRequest => ({
  name: '',
  amountUsd: 100,
  period: 'month',
  timeZone: 'UTC',
  scope: 'project',
  scopeValue: '',
  thresholds: [80, 100],
  channelIds: [],
  includeClaudeCode: true,
  includeLlmApps: false,
});

export const toBudgetRequest = (budget: AiBudget): AiBudgetRequest => ({
  name: budget.name,
  amountUsd: budget.amountUsd,
  period: budget.period,
  timeZone: budget.timeZone,
  scope: budget.scope,
  scopeValue: budget.scopeValue,
  thresholds: budget.thresholds,
  channelIds: budget.channelIds,
  includeClaudeCode: budget.includeClaudeCode,
  includeLlmApps: budget.includeLlmApps,
});
