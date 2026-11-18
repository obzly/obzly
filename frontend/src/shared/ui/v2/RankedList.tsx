import type { JSX, ReactNode } from 'react';

export interface RankedRow {
  key: string;
  label: ReactNode;
  value: number;
  display?: ReactNode;
  extra?: ReactNode;
  color?: string;
  onClick?: () => void;
}

interface Props {
  rows: RankedRow[];
  empty?: ReactNode;
}

/** Top-N list with a bar per row; the bar is relative to the largest value. */
export const RankedList = ({ rows, empty }: Props): JSX.Element => {
  if (!rows.length) {
    return (
      <div className="py-4 text-center text-sm text-gray-500 dark:text-gray-400">
        {empty ?? 'Nothing yet'}
      </div>
    );
  }
  const max = Math.max(...rows.map((r) => r.value)) || 1;
  return (
    <div className="flex flex-col gap-3">
      {rows.map((r) => {
        const inner = (
          <>
            <span className="flex justify-between gap-3 text-sm">
              <span className="min-w-0 truncate text-gray-800 dark:text-gray-100">{r.label}</span>
              <span className="whitespace-nowrap text-gray-900 dark:text-gray-50">
                <strong className="font-semibold">{r.display ?? r.value.toLocaleString()}</strong>
                {r.extra && (
                  <span className="ml-1.5 text-xs text-gray-500 dark:text-gray-400">{r.extra}</span>
                )}
              </span>
            </span>
            <span className="block h-1.5 overflow-hidden rounded-full bg-[var(--v2-track)]">
              <span
                className="block h-full rounded-full"
                style={{
                  width: `${Math.max(2, (r.value / max) * 100)}%`,
                  background: r.color ?? 'var(--v2-accent)',
                }}
              />
            </span>
          </>
        );
        return r.onClick ? (
          <button
            key={r.key}
            type="button"
            onClick={r.onClick}
            className="flex cursor-pointer flex-col gap-1.5 text-left"
          >
            {inner}
          </button>
        ) : (
          <div key={r.key} className="flex flex-col gap-1.5">
            {inner}
          </div>
        );
      })}
    </div>
  );
};
