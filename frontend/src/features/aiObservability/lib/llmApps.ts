import type {
  AiUsageTimeseriesPoint,
  LlmBreakdownRow,
  LlmTimeseriesPoint,
} from '../../../entity/aiUsage';
import { formatUsd } from './format';

export const NO_PRICE = 'no price set';

/** A breakdown row's cost: tool and agent spans have no tokens, unpriced models no cost. */
export const costCell = (row: Pick<LlmBreakdownRow, 'costUsd' | 'hasTokens' | 'unpriced'>) => {
  if (!row.hasTokens) return '-';
  if (row.costUsd === null) return NO_PRICE;
  return row.unpriced ? `${formatUsd(row.costUsd)} + unpriced` : formatUsd(row.costUsd);
};

export const errorRate = (errors: number, calls: number): number => (calls ? errors / calls : 0);

/** LLM timeseries as UsageChart points: tokens (input + output) per model. */
export const toUsagePoints = (points: LlmTimeseriesPoint[]): AiUsageTimeseriesPoint[] =>
  points.map((point) => ({
    bucket: point.bucket,
    key: point.model || '(no model)',
    costUsd: 0,
    tokens: point.inputTokens + point.outputTokens,
    requests: point.calls,
  }));

/** A price cell's value: '' (not set) or a non-negative number. */
export const parsePrice = (value: string): number | null | 'invalid' => {
  const trimmed = value.trim();
  if (trimmed === '') return null;
  const parsed = Number(trimmed);
  return Number.isFinite(parsed) && parsed >= 0 ? parsed : 'invalid';
};
