import { LoadingOutlined } from '@ant-design/icons';
import { Spin, Tooltip } from 'antd';
import type { ReactNode } from 'react';

import type { PillTone } from '../lib/capacity';
import type { Loaded } from '../lib/useLoad';

/** A panel's link to the page behind it, in the accent colour. */
export const PanelLink = ({ onClick, children }: { onClick: () => void; children: ReactNode }) => (
  <button
    type="button"
    onClick={onClick}
    className="text-accent-600 hover:text-accent-700 dark:text-accent-400 dark:hover:text-accent-300 cursor-pointer border-0 bg-transparent p-0 text-sm font-medium"
  >
    {children}
  </button>
);

const PILL: Record<PillTone, string> = {
  // Status colours, the same in every theme.
  good: 'bg-[var(--v2-green-soft,rgba(16,185,129,0.14))] text-[var(--v2-green,#10b981)]',
  warn: 'bg-amber-500/15 text-[var(--v2-orange)]',
  bad: 'bg-red-500/15 text-[var(--v2-red)]',
  neutral: 'bg-gray-100 text-gray-500 dark:bg-gray-700 dark:text-gray-300',
};

export const Pill = ({ tone, children }: { tone: PillTone; children: ReactNode }) => (
  <span
    className={`inline-flex items-center rounded-full px-2 py-0.5 text-xs font-medium whitespace-nowrap ${PILL[tone]}`}
  >
    {children}
  </span>
);

/**
 * What a panel shows before its data: a spinner while loading, "n/a" with the reason when the
 * request failed. Renders children only with data.
 */
export const PanelBody = <T,>({
  state,
  children,
  minHeight = 120,
}: {
  state: Loaded<T>;
  children: (data: T) => ReactNode;
  minHeight?: number;
}) => {
  if (state.data !== undefined) return <>{children(state.data)}</>;
  if (state.loading) {
    return (
      <div className="flex items-center justify-center" style={{ minHeight }}>
        <Spin indicator={<LoadingOutlined spin />} />
      </div>
    );
  }
  return (
    <div
      className="flex flex-col items-center justify-center gap-1 text-center"
      style={{ minHeight }}
    >
      <span className="text-lg font-semibold text-gray-400 dark:text-gray-500">n/a</span>
      {state.error && (
        <Tooltip title={state.error}>
          <span className="max-w-[260px] truncate text-xs text-gray-500 dark:text-gray-400">
            {state.error}
          </span>
        </Tooltip>
      )}
    </div>
  );
};

export const Muted = ({ children }: { children: ReactNode }) => (
  <div className="py-6 text-center text-sm text-gray-500 dark:text-gray-400">{children}</div>
);
