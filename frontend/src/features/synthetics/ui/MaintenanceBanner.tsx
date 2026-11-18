import { ToolOutlined } from '@ant-design/icons';
import { Button } from 'antd';

import type { CheckView, MaintenanceOccurrence } from '../../../entity/synthetics';
import { MAINTENANCE_COLOR, relevantOccurrence } from '../lib/maintenance';
import { RANGE_SECONDS, type RangeKey } from '../lib/useMonitorData';
import { useNow } from '../lib/useNow';

const when = new Intl.DateTimeFormat(undefined, {
  weekday: 'short',
  hour: '2-digit',
  minute: '2-digit',
});
const until = new Intl.DateTimeFormat(undefined, { hour: '2-digit', minute: '2-digit' });

export const MaintenanceBanner = ({
  check,
  maintenance,
  range,
  onOpen,
}: {
  check: CheckView;
  maintenance: MaintenanceOccurrence[];
  range: RangeKey;
  onOpen?: (windowId: string) => void;
}) => {
  const now = useNow();
  const item = relevantOccurrence(check.id, maintenance, now, now - RANGE_SECONDS[range] * 1000);
  if (!item) return null;
  const site = check.destination.siteId || 'its Site';
  const active = Date.parse(item.start) <= now && Date.parse(item.end) > now;
  return (
    <div className="mb-4 flex flex-wrap items-center gap-3 rounded-2xl border border-slate-200 bg-slate-50 px-4 py-3 text-sm text-gray-700 dark:border-slate-700 dark:bg-slate-900/40 dark:text-gray-200">
      <BannerIcon />
      <span className="min-w-0 flex-1">
        Maintenance <b className="font-semibold">{item.name}</b>,{' '}
        {when.format(Date.parse(item.start))} to {until.format(Date.parse(item.end))}
        {item.reason ? ` (${item.reason})` : ''}:{' '}
        {active || Date.parse(item.start) > now
          ? `alerts muted on ${site}, runs still recorded.`
          : 'alerts were muted and uptime leaves it out.'}
      </span>
      {onOpen && (
        <Button type="link" size="small" className="px-0" onClick={() => onOpen(item.windowId)}>
          Open window
        </Button>
      )}
    </div>
  );
};

const BannerIcon = () => (
  <span
    className="inline-flex h-8 w-8 shrink-0 items-center justify-center rounded-xl text-white"
    style={{ background: MAINTENANCE_COLOR }}
    aria-hidden
  >
    <ToolOutlined />
  </span>
);

/** Page-wide: the windows active now, each with how many monitors it covers. */
export const ActiveMaintenanceBanner = ({
  occurrences,
  onOpen,
}: {
  occurrences: MaintenanceOccurrence[];
  onOpen: (windowId: string) => void;
}) => {
  const now = useNow();
  const active = occurrences.filter(
    (item) => Date.parse(item.start) <= now && Date.parse(item.end) > now,
  );
  if (active.length === 0) return null;
  return (
    <div className="flex flex-col gap-2">
      {active.map((item) => {
        const covered = item.monitorIds?.length ?? 0;
        return (
          <div
            key={`${item.windowId}|${item.start}`}
            className="flex flex-wrap items-center gap-3 rounded-2xl border border-slate-200 bg-slate-50 px-4 py-3 text-sm text-gray-700 dark:border-slate-700 dark:bg-slate-900/40 dark:text-gray-200"
          >
            <BannerIcon />
            <span className="min-w-0 flex-1">
              <b className="font-semibold">
                Maintenance now until {until.format(Date.parse(item.end))}
              </b>{' '}
              <span className="text-gray-500 dark:text-gray-400">
                · {item.name}
                {item.reason ? ` (${item.reason})` : ''}: {covered} monitor
                {covered === 1 ? '' : 's'} keep running, but failures don&apos;t alert or count
                against uptime.
              </span>
            </span>
            <Button type="link" size="small" className="px-0" onClick={() => onOpen(item.windowId)}>
              Open window
            </Button>
          </div>
        );
      })}
    </div>
  );
};
