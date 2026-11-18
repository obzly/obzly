import { LoadingOutlined } from '@ant-design/icons';
import { Spin, Tooltip } from 'antd';
import type { ComponentProps, ReactNode } from 'react';

import type { Coverage } from '../../../entity/cost';
import { KpiTile } from '../../../shared/ui/v2';
import type { SiteOutcome } from '../lib/merge';

export const SourceBadge = ({ source }: { source: 'export' | 'api' }) =>
  source === 'export' ? (
    <Tooltip title="FOCUS export: resource level, final once the provider closes the day">
      <span className="rounded-full bg-[var(--v2-ok-soft)] px-2 py-0.5 text-xs font-medium whitespace-nowrap text-[var(--v2-accent)]">
        Export
      </span>
    </Tooltip>
  ) : (
    <Tooltip title="Billing API: by account, service and region; no single resources">
      <span className="rounded-full bg-sky-500/15 px-2 py-0.5 text-xs font-medium whitespace-nowrap text-[var(--v2-blue)]">
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
        className={`ml-1 inline-block h-2 w-2 rounded-full ${coverage === 'partial' ? 'bg-[var(--v2-orange)]' : 'border border-gray-400'}`}
      />
    </Tooltip>
  ) : null;

/** Sites that didn't answer, named beside the figures they're missing from. */
export const SiteErrors = ({ outcomes }: { outcomes: SiteOutcome<unknown>[] }) => {
  const failed = outcomes.filter((outcome) => outcome.error);
  if (failed.length === 0) return null;
  return (
    <div
      role="status"
      className="mb-3 flex items-start gap-2.5 rounded-xl border border-amber-300 bg-amber-50 px-3 py-2 text-sm text-amber-800 dark:border-amber-700/60 dark:bg-amber-950/40 dark:text-amber-300"
    >
      <span
        aria-hidden="true"
        className="mt-0.5 flex h-4 w-4 shrink-0 items-center justify-center rounded-full bg-[var(--v2-orange)] text-[11px] font-bold text-white"
      >
        !
      </span>
      <span>
        <strong className="font-semibold">Not included:</strong>{' '}
        {failed
          .map((outcome) => `${outcome.site.siteName || 'this Site'} (${outcome.error})`)
          .join('; ')}
      </span>
    </div>
  );
};

/** A v2 KPI tile whose label explains itself on hover; the tile's own text says the essentials. */
export const TipTile = ({
  tooltip,
  ...tile
}: ComponentProps<typeof KpiTile> & { tooltip?: string }) =>
  tooltip ? (
    <Tooltip title={tooltip}>
      <div className="grid min-w-0">
        <KpiTile {...tile} />
      </div>
    </Tooltip>
  ) : (
    <KpiTile {...tile} />
  );

/** Kept for callers of the old tile: the same figures as a v2 KPI tile. */
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
}) => <TipTile tooltip={tooltip} label={label} value={value} note={hint} />;

/** The KPI row: as many tiles per line as fit. */
export const TileRow = ({ label, children }: { label: string; children: ReactNode }) => (
  <section aria-label={label} className="grid grid-cols-[repeat(auto-fit,minmax(200px,1fr))] gap-4">
    {children}
  </section>
);

/** The page's views as v2 pill tabs. */
export const ViewTabs = <View extends string>({
  label,
  value,
  onChange,
  items,
}: {
  label: string;
  value: View;
  onChange: (view: View) => void;
  items: { key: View; label: string }[];
}) => (
  <div role="tablist" aria-label={label} className="flex flex-wrap gap-1">
    {items.map((item) => (
      <button
        key={item.key}
        type="button"
        role="tab"
        aria-selected={item.key === value}
        onClick={() => onChange(item.key)}
        className={`min-h-9 cursor-pointer rounded-xl border px-3.5 py-1.5 text-sm ${
          item.key === value
            ? 'border-gray-200 bg-gray-100 font-medium text-gray-900 dark:border-gray-600 dark:bg-gray-700 dark:text-gray-50'
            : 'border-transparent bg-transparent text-gray-500 hover:bg-gray-100 hover:text-gray-800 dark:text-gray-400 dark:hover:bg-gray-700/60 dark:hover:text-gray-100'
        }`}
      >
        {item.label}
      </button>
    ))}
  </div>
);

export const Loading = ({ className = 'py-16' }: { className?: string }) => (
  <div className={`flex justify-center ${className}`}>
    <Spin indicator={<LoadingOutlined spin />} />
  </div>
);

/** A muted "n/a" for a value nobody knows, never a zero. */
export const NotAvailable = ({ tooltip, text = 'n/a' }: { tooltip?: string; text?: string }) =>
  tooltip ? (
    <Tooltip title={tooltip}>
      <span className="text-gray-400 dark:text-gray-500">{text}</span>
    </Tooltip>
  ) : (
    <span className="text-gray-400 dark:text-gray-500">{text}</span>
  );
