import React from 'react';

import type { InsightTone } from '../lib/logInsights';
import { LOGS_VIEWS } from '../lib/logsViews';

export interface SegmentOption<T extends string | number> {
  value: T;
  label: React.ReactNode;
}

const TONE_CLASS: Record<InsightTone, string> = {
  bad: 'bg-red-500/15 text-[var(--v2-red)]',
  warn: 'bg-orange-500/15 text-[var(--v2-orange)]',
  info: 'bg-blue-500/15 text-[var(--v2-blue)]',
  neutral: 'bg-gray-100 text-gray-500 dark:bg-gray-700 dark:text-gray-300',
};

/** The Logs header's view tabs. */
export const LogsViewTabs = ({
  value,
  onChange,
}: {
  value: string;
  onChange: (view: string) => void;
}): React.JSX.Element => (
  <div
    role="tablist"
    aria-label="Logs views"
    className="inline-flex flex-wrap gap-1 rounded-xl border border-gray-200 bg-gray-50 p-1 dark:border-gray-700 dark:bg-gray-900"
  >
    {LOGS_VIEWS.map((tab) => {
      const isActive = tab.key === value;
      return (
        <button
          key={tab.key}
          type="button"
          role="tab"
          aria-selected={isActive}
          onClick={() => onChange(tab.key)}
          className={`focus-visible:outline-accent-500 inline-flex cursor-pointer items-center rounded-lg px-3.5 py-1.5 text-sm font-medium transition-colors focus-visible:outline-2 focus-visible:outline-offset-1 ${
            isActive
              ? 'bg-accent-600 text-white shadow-sm'
              : 'text-gray-700 hover:bg-white hover:text-gray-900 dark:text-gray-200 dark:hover:bg-gray-800 dark:hover:text-white'
          }`}
        >
          {tab.label}
          {tab.isNew && (
            <span
              className={`ml-1.5 rounded-full px-1.5 text-[11px] leading-4 font-semibold ${
                isActive
                  ? 'bg-white/25 text-white'
                  : 'bg-accent-500/15 text-accent-700 dark:text-accent-300'
              }`}
            >
              v2
            </span>
          )}
        </button>
      );
    })}
  </div>
);

/** A small segmented control: the selected option filled with the accent. */
export const SegmentedControl = <T extends string | number>({
  value,
  options,
  onChange,
  label,
  radio,
}: {
  value: T;
  options: SegmentOption<T>[];
  onChange: (value: T) => void;
  label: string;
  /** role="radiogroup" instead of a group of toggle buttons. */
  radio?: boolean;
}): React.JSX.Element => (
  <span
    role={radio ? 'radiogroup' : 'group'}
    aria-label={label}
    className="inline-flex flex-wrap rounded-lg border border-gray-200 p-0.5 dark:border-gray-700"
  >
    {options.map((option) => {
      const isActive = option.value === value;
      return (
        <button
          key={String(option.value)}
          type="button"
          role={radio ? 'radio' : undefined}
          aria-checked={radio ? isActive : undefined}
          aria-pressed={radio ? undefined : isActive}
          onClick={() => onChange(option.value)}
          className={`cursor-pointer rounded-md px-2.5 py-1 text-xs ${
            isActive
              ? 'bg-accent-600 font-medium text-white'
              : 'text-gray-500 hover:text-gray-800 dark:text-gray-400 dark:hover:text-gray-100'
          }`}
        >
          {option.label}
        </button>
      );
    })}
  </span>
);

/** A soft pill coloured by tone. */
export const TonePill = ({
  text,
  tone,
  className = '',
}: {
  text: React.ReactNode;
  tone: InsightTone;
  className?: string;
}): React.JSX.Element => (
  <span
    className={`inline-flex items-center rounded-full px-2 py-0.5 text-xs font-medium whitespace-nowrap ${TONE_CLASS[tone]} ${className}`}
  >
    {text}
  </span>
);

/** A thin bar filled to share (0-1). */
export const ShareBar = ({
  share,
  color,
  className = 'h-1.5',
}: {
  share: number;
  color: string;
  className?: string;
}): React.JSX.Element => (
  <span
    className={`block w-full overflow-hidden rounded-full bg-[var(--v2-track)] ${className}`}
    aria-hidden="true"
  >
    <span
      className="block h-full rounded-full"
      style={{ width: `${Math.max(0, Math.min(share, 1)) * 100}%`, background: color }}
    />
  </span>
);

/** Stacked columns (one per bucket), each segment sized by its value. */
export const StackedColumns = ({
  columns,
  height = 150,
  label,
  gap = 3,
}: {
  columns: { key: string; title: string; segments: { value: number; color: string }[] }[];
  height?: number;
  label: string;
  gap?: number;
}): React.JSX.Element => {
  const max = Math.max(1, ...columns.map((c) => c.segments.reduce((sum, s) => sum + s.value, 0)));
  return (
    <div
      role="img"
      aria-label={label}
      className="flex items-end border-b border-gray-200 dark:border-gray-700"
      style={{ height, gap }}
    >
      {columns.map((column) => (
        <span
          key={column.key}
          title={column.title}
          className="flex h-full min-w-0 flex-1 flex-col-reverse"
        >
          {column.segments.map((segment, i) =>
            segment.value > 0 ? (
              <span
                key={i}
                style={{ height: `${(segment.value / max) * 100}%`, background: segment.color }}
              />
            ) : null,
          )}
        </span>
      ))}
    </div>
  );
};
