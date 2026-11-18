import { useMemo } from 'react';

import type { CheckView, MaintenanceOccurrence } from '../../../entity/synthetics';
import { useTheme } from '../../../shared/theme';
import { Card, StatusStrip, type StripState } from '../../../shared/ui/v2';
import { formatMs, formatPercent } from '../../synthetics/lib/format';
import { type MonitorStatus, STATUS_ORDER } from '../../synthetics/lib/status';
import { STRIP_SLOTS, type SlotStatus, useMonitorData } from '../../synthetics/lib/useMonitorData';
import type { Loaded } from '../lib/useLoad';
import { Muted, PanelBody, PanelLink } from './parts';

const TOP = 5;
const NO_MAINTENANCE: MaintenanceOccurrence[] = [];

const SLOT_LABEL: Record<SlotStatus, string> = {
  up: 'Up',
  warn: 'Degraded',
  down: 'Down',
  maintenance: 'Maintenance',
  none: 'No runs',
};

const EMPTY_STRIP: SlotStatus[] = Array<SlotStatus>(STRIP_SLOTS).fill('none');

const SLOT_STATE: Record<SlotStatus, StripState> = {
  up: 'ok',
  warn: 'warn',
  down: 'fail',
  maintenance: 'none',
  none: 'none',
};

interface Props {
  /** This project's monitors (their results land in it) and the maintenance windows. */
  state: Loaded<{ checks: CheckView[]; maintenance: MaintenanceOccurrence[] }>;
  refresh: number;
  onOpen: () => void;
}

/** Synthetic checks run from a control plane; a standalone Site has none. */
export const SyntheticsPanel = ({ state, refresh, onOpen }: Props) => (
  <Card
    title="Synthetic checks"
    subtitle="Last 24 h, one strip per monitor"
    extra={<PanelLink onClick={onOpen}>Monitors</PanelLink>}
  >
    <PanelBody state={state} minHeight={240}>
      {(data) =>
        data.checks.length === 0 ? (
          <Muted>No monitor sends its results to this project</Muted>
        ) : (
          <MonitorRows checks={data.checks} maintenance={data.maintenance} refresh={refresh} />
        )
      }
    </PanelBody>
  </Card>
);

export const SyntheticsUnavailable = () => (
  <Card title="Synthetic checks" subtitle="Last 24 h, one strip per monitor">
    <Muted>Synthetic checks are set up on a control plane</Muted>
  </Card>
);

const MonitorRows = ({
  checks,
  maintenance,
  refresh,
}: {
  checks: CheckView[];
  maintenance: MaintenanceOccurrence[];
  refresh: number;
}) => {
  const { status } = useTheme();
  const result = useMonitorData(checks, '24h', refresh, maintenance ?? NO_MAINTENANCE);
  const dot: Record<MonitorStatus, string> = {
    up: status.ok,
    warn: status.warn,
    down: status.bad,
    impacted: status.warn,
    maintenance: status.info,
    off: status.noData,
    none: status.noData,
  };
  const rows = useMemo(
    () =>
      checks
        .map((check) => ({ check, data: result.data.get(check.id) }))
        .sort(
          (a, b) =>
            STATUS_ORDER[a.data?.state.status ?? 'none'] -
              STATUS_ORDER[b.data?.state.status ?? 'none'] ||
            a.check.name.localeCompare(b.check.name),
        )
        .slice(0, TOP),
    [checks, result.data],
  );

  if (result.loading && result.data.size === 0) {
    return <Muted>Loading results…</Muted>;
  }
  return (
    <div className="flex flex-col gap-3.5">
      {result.errors.length > 0 && (
        <span className="text-xs text-[var(--v2-orange)]">
          Some results couldn&apos;t be read: {result.errors.join('; ')}
        </span>
      )}
      {rows.map(({ check, data }) => {
        const certOnly = check.type === 'tls' && data?.certDays !== undefined;
        return (
          <span key={check.id} className="flex flex-col gap-1.5">
            <span className="flex items-center justify-between gap-3 text-sm">
              <span className="flex min-w-0 items-center gap-2">
                <span
                  className="h-2 w-2 shrink-0 rounded-full"
                  style={{ background: dot[data?.state.status ?? 'none'] }}
                />
                <span className="truncate text-gray-800 dark:text-gray-100">{check.name}</span>
              </span>
              <span className="whitespace-nowrap">
                <strong className="font-semibold text-gray-900 dark:text-gray-50">
                  {data?.uptime === undefined ? 'n/a' : formatPercent(data.uptime)}
                </strong>{' '}
                <span className="text-xs text-gray-500 dark:text-gray-400">
                  {certOnly
                    ? `expires in ${Math.floor(data!.certDays!)} d`
                    : data?.p95Ms === undefined
                      ? 'p95 n/a'
                      : `p95 ${formatMs(data.p95Ms)}`}
                </span>
              </span>
            </span>
            <StatusStrip
              height={12}
              label={`${check.name}, last 24 hours`}
              cells={(data?.strip ?? EMPTY_STRIP).map((slot) => ({
                state: SLOT_STATE[slot],
                title: SLOT_LABEL[slot],
              }))}
            />
          </span>
        );
      })}
      {checks.length > TOP && (
        <span className="text-xs text-gray-500 dark:text-gray-400">
          {checks.length - TOP} more monitors on the Synthetics page
        </span>
      )}
    </div>
  );
};
