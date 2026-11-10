import { Tooltip } from 'antd';
import type { ReactNode } from 'react';

import type { Coverage } from '../../../entity/cost';
import type { SiteOutcome } from '../lib/merge';

export const SourceBadge = ({ source }: { source: 'export' | 'api' }) =>
  source === 'export' ? (
    <Tooltip title="FOCUS export: resource level, final once the provider closes the day">
      <span className="rounded bg-emerald-50 px-1.5 py-px text-xs font-medium text-emerald-700 dark:bg-emerald-950 dark:text-emerald-300">
        Export
      </span>
    </Tooltip>
  ) : (
    <Tooltip title="Billing API: by account, service and region; no single resources">
      <span className="rounded bg-sky-50 px-1.5 py-px text-xs font-medium text-sky-700 dark:bg-sky-950 dark:text-sky-300">
        API estimate
      </span>
    </Tooltip>
  );

const COVERAGE_TEXT: Record<Exclude<Coverage, 'full' | ''>, string> = {
  partial: 'Some connections in scope have no data for part of this period',
  none: 'No connection in scope has data for this period yet',
};

/** Marks a period that isn't fully covered, so a gap never reads as zero spend. */
export const CoverageMark = ({ coverage }: { coverage: Coverage }) =>
  coverage === 'partial' || coverage === 'none' ? (
    <Tooltip title={COVERAGE_TEXT[coverage]}>
      <span
        role="img"
        aria-label={coverage === 'partial' ? 'Partly covered' : 'Not covered'}
        className={`ml-1 inline-block h-2 w-2 rounded-full ${coverage === 'partial' ? 'bg-amber-500' : 'border border-gray-400'}`}
      />
    </Tooltip>
  ) : null;

/** Sites that didn't answer, named beside the figures they're missing from. */
export const SiteErrors = ({ outcomes }: { outcomes: SiteOutcome<unknown>[] }) => {
  const failed = outcomes.filter((outcome) => outcome.error);
  if (failed.length === 0) return null;
  return (
    <div className="mb-3 rounded border border-amber-300 bg-amber-50 px-3 py-2 text-sm text-amber-800 dark:border-amber-800 dark:bg-amber-950 dark:text-amber-300">
      Not included:{' '}
      {failed
        .map((outcome) => `${outcome.site.siteName || 'this Site'} (${outcome.error})`)
        .join('; ')}
    </div>
  );
};

export const Tile = ({
  label,
  value,
  hint,
  tooltip,
}: {
  label: string;
  value: ReactNode;
  hint?: ReactNode;
  tooltip?: string;
}) => (
  <div className="rounded-lg border border-gray-200 bg-white p-4 dark:border-gray-700 dark:bg-gray-800">
    <div className="text-xs text-gray-500 dark:text-gray-400">
      {tooltip ? (
        <Tooltip title={tooltip}>
          <span className="cursor-help border-b border-dotted border-gray-400">{label}</span>
        </Tooltip>
      ) : (
        label
      )}
    </div>
    <div className="mt-1 text-2xl font-semibold text-gray-900 tabular-nums dark:text-gray-100">
      {value}
    </div>
    {hint && <div className="mt-1 text-xs text-gray-500 dark:text-gray-400">{hint}</div>}
  </div>
);

/** A ranked list with each amount's share drawn behind it. */
export const RankList = ({
  rows,
  format,
  onClick,
}: {
  rows: { key: string; label: string; amount: number }[];
  format: (amount: number) => string;
  onClick?: (key: string) => void;
}) => {
  const max = Math.max(...rows.map((row) => row.amount), 0);
  if (rows.length === 0) {
    return <div className="py-6 text-center text-sm text-gray-500 dark:text-gray-400">No cost</div>;
  }
  return (
    <div className="flex flex-col gap-1">
      {rows.map((row) => (
        <button
          key={row.key}
          type="button"
          disabled={!onClick}
          onClick={() => onClick?.(row.key)}
          className="relative flex items-center justify-between gap-3 overflow-hidden rounded px-2 py-1.5 text-left text-sm enabled:cursor-pointer enabled:hover:bg-gray-50 dark:enabled:hover:bg-gray-700"
        >
          <span
            className="absolute inset-y-0 left-0 bg-emerald-100 dark:bg-emerald-900/40"
            style={{ width: `${max ? (row.amount / max) * 100 : 0}%` }}
          />
          <span className="relative truncate text-gray-800 dark:text-gray-200">{row.label}</span>
          <span className="relative shrink-0 text-gray-700 tabular-nums dark:text-gray-300">
            {format(row.amount)}
          </span>
        </button>
      ))}
    </div>
  );
};
