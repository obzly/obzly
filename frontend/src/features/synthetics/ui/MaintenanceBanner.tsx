import { ToolOutlined } from '@ant-design/icons';
import { Button } from 'antd';

import type { CheckView, MaintenanceOccurrence } from '../../../entity/synthetics';
import { relevantOccurrence } from '../lib/maintenance';
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
    <div className="mb-4 flex flex-wrap items-center gap-2 rounded-md border border-slate-200 bg-slate-50 px-3 py-2 text-sm text-gray-700 dark:border-slate-700 dark:bg-slate-900/40 dark:text-gray-200">
      <ToolOutlined className="text-slate-500" aria-hidden />
      <span>
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
