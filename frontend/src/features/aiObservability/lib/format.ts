import dayjs from 'dayjs';

import { getUserShortTimeFormat } from '../../../shared/time';

const compact = new Intl.NumberFormat(undefined, { notation: 'compact', maximumFractionDigits: 1 });

export const formatUsd = (value: number): string => {
  if (value === 0) return '$0.00';
  if (Math.abs(value) < 0.01) return `$${value.toFixed(4)}`;
  return `$${value.toLocaleString(undefined, { minimumFractionDigits: 2, maximumFractionDigits: 2 })}`;
};

export const formatCount = (value: number): string =>
  value < 10_000 ? value.toLocaleString() : compact.format(value);

export const formatMs = (value: number): string =>
  value >= 1000 ? `${(value / 1000).toFixed(1)} s` : `${Math.round(value)} ms`;

export const formatPercent = (value: number): string => `${(value * 100).toFixed(1)}%`;

export const formatTime = (unixMs: number): string =>
  unixMs ? dayjs(unixMs).format(getUserShortTimeFormat().format) : '';
