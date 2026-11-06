export type BudgetPeriod = 'day' | 'week' | 'month';
export type BudgetScope = 'project' | 'each_user' | 'each_model' | 'user' | 'model';

export interface AiBudget {
  id: string;
  projectId: string;
  name: string;
  amountUsd: number;
  period: BudgetPeriod;
  timeZone: string;
  scope: BudgetScope;
  scopeValue: string;
  /** Percents of the amount, ascending. */
  thresholds: number[];
  channelIds: string[];
  includeClaudeCode: boolean;
  includeLlmApps: boolean;
  createdAt: string;
  updatedAt: string;
}

export interface AiBudgetRequest {
  name: string;
  amountUsd: number;
  period: BudgetPeriod;
  timeZone: string;
  scope: BudgetScope;
  scopeValue: string;
  thresholds: number[];
  channelIds: string[];
  includeClaudeCode: boolean;
  includeLlmApps: boolean;
}

export interface AiBudgetGroup {
  key: string;
  spentUsd: number;
  percent: number;
}

export interface AiBudgetStatus {
  budgetId: string;
  periodStart: number;
  periodEnd: number;
  /** The scope's total (every user or model for per-user/per-model budgets). */
  spentUsd: number;
  /** The budget's, or the highest-spending user's or model's. */
  percent: number;
  forecastUsd: number;
  groups: number;
  groupsWarning: number;
  groupsOver: number;
  top: AiBudgetGroup[];
  error?: string;
}

export interface AiModelPrice {
  id: string;
  projectId: string;
  /** Exact name, or a prefix ending in "*". */
  model: string;
  inputPerMillion: number;
  outputPerMillion: number;
  cachedInputPerMillion: number | null;
  /** LLM calls in the last 24 h this price applied to; -1 when unknown. */
  seenCalls: number;
}

export interface AiModelPriceRequest {
  model: string;
  inputPerMillion: number;
  outputPerMillion: number;
  cachedInputPerMillion: number | null;
}

export interface AiModelPricesResponse {
  prices: AiModelPrice[];
  unpricedModels: { model: string; calls: number }[];
  seenError?: string;
}

export interface FillCommonPricesResponse {
  added: number;
  asOf: string;
  models: string[];
}
