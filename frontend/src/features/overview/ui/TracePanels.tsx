import { Tooltip } from 'antd';
import { useMemo } from 'react';

import type { Session } from '../../../entity/correlation';
import type { ServiceErrorsResponse, TraceSummaryResponse } from '../../../entity/traces';
import { useTheme } from '../../../shared/theme';
import { formatInTimezone, useDisplayTimezone } from '../../../shared/time';
import { Card, ChangePill } from '../../../shared/ui/v2';
import {
  type OverviewRange,
  compactNumber,
  errorRate,
  peakBar,
  requestBars,
  serviceErrorRows,
} from '../lib/overview';
import type { Loaded } from '../lib/useLoad';
import { Muted, PanelBody, PanelLink } from './parts';

// The service map's bands: 5%+ errors is failing, 1%+ degraded.
const rateTone = (rate: number): 'bad' | 'warn' | 'ok' =>
  rate >= 5 ? 'bad' : rate >= 1 ? 'warn' : 'ok';

export const ServiceErrorsPanel = ({
  state,
  onOpenService,
  onOpenServiceMap,
}: {
  state: Loaded<ServiceErrorsResponse>;
  onOpenService: (name: string) => void;
  onOpenServiceMap: () => void;
}) => {
  const { status } = useTheme();
  return (
    <Card
      title="Services with the most errors"
      subtitle={
        state.data?.clamped
          ? `Error rate of entry spans, last ${clampedDays(state.data)} days (the longest range counted), change vs the ${clampedDays(state.data)} days before`
          : 'Error rate of entry spans, change vs the previous period'
      }
      extra={<PanelLink onClick={onOpenServiceMap}>Service map</PanelLink>}
    >
      <PanelBody state={state} minHeight={220}>
        {(data) => {
          const rows = serviceErrorRows(data);
          if (rows.length === 0) return <Muted>No traced requests in this range</Muted>;
          // Bars are relative to the worst rate, with 5% (failing) as the floor of the scale.
          const scale = Math.max(5, ...rows.map((row) => row.rate));
          return (
            <div className="flex flex-col gap-3.5">
              {rows.map((row) => {
                const color = status[rateTone(row.rate)];
                return (
                  <button
                    key={row.name}
                    type="button"
                    onClick={() => onOpenService(row.name)}
                    className="flex cursor-pointer flex-col gap-1.5 border-0 bg-transparent p-0 text-left"
                    aria-label={`${row.name}: ${row.rate.toFixed(1)}% errors, open its traces`}
                  >
                    <span className="flex items-baseline justify-between gap-3 text-sm">
                      <span className="min-w-0 truncate text-gray-800 dark:text-gray-100">
                        {row.name}
                      </span>
                      <span className="flex items-center gap-2 whitespace-nowrap">
                        <Tooltip
                          title={`${row.errors.toLocaleString()} of ${row.requests.toLocaleString()} requests failed`}
                        >
                          <strong className="font-semibold text-gray-900 dark:text-gray-50">
                            {row.rate.toFixed(1)}%
                          </strong>
                        </Tooltip>
                        {row.change ? (
                          <ChangePill text={row.change.text} tone={row.change.tone} />
                        ) : (
                          <span className="text-xs text-gray-400 dark:text-gray-500">new</span>
                        )}
                      </span>
                    </span>
                    <span className="block h-1.5 overflow-hidden rounded-full bg-[var(--v2-track)]">
                      <span
                        className="block h-full rounded-full"
                        style={{
                          width: `${Math.max(2, (row.rate / scale) * 100)}%`,
                          background: color,
                        }}
                      />
                    </span>
                  </button>
                );
              })}
            </div>
          );
        }}
      </PanelBody>
    </Card>
  );
};

const AXIS_FORMAT: Record<OverviewRange, string> = {
  '1h': 'HH:mm',
  '24h': 'HH:mm',
  '7d': 'ddd D',
  '30d': 'MMM D',
};

const TIP_FORMAT: Record<OverviewRange, string> = {
  '1h': 'HH:mm',
  '24h': 'MMM D HH:mm',
  '7d': 'MMM D HH:mm',
  '30d': 'MMM D HH:mm',
};

const BAR_HEIGHT = 170;

export const RequestsPanel = ({
  state,
  sessions,
  from,
  range,
  onOpen,
}: {
  state: Loaded<TraceSummaryResponse>;
  sessions?: Session[];
  from: number;
  range: OverviewRange;
  onOpen: () => void;
}) => {
  const { status } = useTheme();
  const [timezone] = useDisplayTimezone();
  const bars = useMemo(
    () => (state.data ? requestBars(state.data, from, sessions) : []),
    [state.data, from, sessions],
  );

  return (
    <Card
      title="Requests and errors"
      subtitle="Traces (root spans) per interval, with the incidents that opened in each"
      extra={
        <span className="flex items-center gap-3 text-xs text-gray-500 dark:text-gray-400">
          <span className="flex items-center gap-1.5">
            <span className="h-2.5 w-2.5 rounded-sm" style={{ background: status.ok }} />
            OK
          </span>
          <span className="flex items-center gap-1.5">
            <span className="h-2.5 w-2.5 rounded-sm" style={{ background: status.bad }} />
            Errors
          </span>
          <span className="flex items-center gap-1.5">
            <span
              aria-hidden="true"
              className="h-2 w-2 rotate-45 rounded-[1px]"
              style={{ background: status.warn }}
            />
            Incident opened
          </span>
          <PanelLink onClick={onOpen}>Traces</PanelLink>
        </span>
      }
    >
      <PanelBody state={state} minHeight={BAR_HEIGHT + 80}>
        {(summary) => {
          const total = summary.current.count;
          if (!total) return <Muted>No traces in this range</Muted>;
          const max = Math.max(1, ...bars.map((bar) => bar.ok + bar.errors));
          const peak = peakBar(bars, summary.stepMs);
          const share = errorRate(summary.current.errors, total);
          const ticks = [0, 0.25, 0.5, 0.75, 1].map((f) =>
            bars.length
              ? bars[Math.min(bars.length - 1, Math.round(f * (bars.length - 1)))]
              : undefined,
          );
          const stats = [
            {
              label: 'Peak',
              value: peak
                ? `${formatInTimezone(peak.at, TIP_FORMAT[range], timezone)} · ${compactNumber(peak.perMinute)} req/min`
                : 'n/a',
            },
            { label: 'Requests', value: compactNumber(total) },
            { label: 'Error share', value: share === undefined ? 'n/a' : `${share.toFixed(1)}%` },
          ];
          return (
            <div className="flex flex-col gap-3">
              <div
                role="img"
                aria-label={`Requests per interval over the ${range}: ${total.toLocaleString()} requests, ${(summary.current.errors ?? 0).toLocaleString()} errors`}
                className="flex items-end gap-[3px]"
                style={{ height: BAR_HEIGHT }}
              >
                {bars.map((bar) => {
                  const count = bar.ok + bar.errors;
                  return (
                    <Tooltip
                      key={bar.at}
                      title={`${formatInTimezone(bar.at, TIP_FORMAT[range], timezone)}: ${count.toLocaleString()} requests, ${bar.errors.toLocaleString()} errors${bar.incidents ? ` · ${bar.incidents} incident${bar.incidents === 1 ? '' : 's'} opened` : ''}`}
                    >
                      <span
                        className="flex min-w-0 flex-1 cursor-default flex-col justify-end overflow-hidden rounded-[3px]"
                        style={{
                          height: `${Math.max(count ? 3 : 1, (count / max) * BAR_HEIGHT)}px`,
                          background: count ? undefined : 'var(--v2-track)',
                        }}
                      >
                        {bar.errors > 0 && (
                          <span
                            style={{
                              height: `${(bar.errors / count) * 100}%`,
                              minHeight: 2,
                              background: status.bad,
                            }}
                          />
                        )}
                        {bar.ok > 0 && <span className="grow" style={{ background: status.ok }} />}
                      </span>
                    </Tooltip>
                  );
                })}
              </div>
              {bars.some((bar) => bar.incidents > 0) && (
                <div
                  aria-label="Incidents opened per interval"
                  className="-mt-1.5 flex h-2.5 items-center gap-[3px]"
                >
                  {bars.map((bar) => (
                    <span key={bar.at} className="flex min-w-0 flex-1 justify-center">
                      {bar.incidents > 0 && (
                        <span
                          title={`${bar.incidents} incident${bar.incidents === 1 ? '' : 's'} opened`}
                          className="h-2 w-2 rotate-45 rounded-[1px]"
                          style={{ background: status.warn }}
                        />
                      )}
                    </span>
                  ))}
                </div>
              )}
              <div className="flex justify-between text-xs text-gray-500 dark:text-gray-400">
                {ticks.map((bar, index) => (
                  <span key={index}>
                    {bar ? formatInTimezone(bar.at, AXIS_FORMAT[range], timezone) : ''}
                  </span>
                ))}
              </div>
              <div className="grid grid-cols-[repeat(auto-fit,minmax(150px,1fr))] gap-3 border-t border-gray-200 pt-3 dark:border-gray-700">
                {stats.map((stat) => (
                  <span key={stat.label} className="flex flex-col">
                    <span className="text-xs text-gray-500 dark:text-gray-400">{stat.label}</span>
                    <strong className="text-sm font-semibold text-gray-900 dark:text-gray-50">
                      {stat.value}
                    </strong>
                  </span>
                ))}
              </div>
            </div>
          );
        }}
      </PanelBody>
    </Card>
  );
};

/** Days a clamped service errors range covers (the server's limit). */
const clampedDays = (data: ServiceErrorsResponse) =>
  Math.round((Date.parse(data.to) - Date.parse(data.from)) / 86_400_000);
