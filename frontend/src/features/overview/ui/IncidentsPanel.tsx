import { CheckOutlined, HistoryOutlined, ThunderboltOutlined } from '@ant-design/icons';
import type { ReactNode } from 'react';

import type { Session } from '../../../entity/correlation';
import { useTheme } from '../../../shared/theme';
import { Card, Donut } from '../../../shared/ui/v2';
import { type IncidentGroup, formatMinutes, summarizeIncidents } from '../lib/overview';
import type { Loaded } from '../lib/useLoad';
import { PanelBody, PanelLink } from './parts';

const WEEK_MS = 7 * 24 * 60 * 60_000;

export const IncidentsPanel = ({
  state,
  now,
  onOpen,
}: {
  state: Loaded<Session[]>;
  now: number;
  onOpen: () => void;
}) => {
  const { status } = useTheme();
  const rows: { key: IncidentGroup; label: string; color: string; icon: ReactNode }[] = [
    { key: 'ongoing', label: 'Ongoing', color: status.bad, icon: <ThunderboltOutlined /> },
    { key: 'recent', label: 'Ended, last 24 h', color: status.warn, icon: <HistoryOutlined /> },
    { key: 'earlier', label: 'Ended earlier', color: status.ok, icon: <CheckOutlined /> },
  ];
  return (
    <Card
      title="Incidents"
      subtitle="Correlated sessions, last 7 days"
      extra={<PanelLink onClick={onOpen}>Incidents</PanelLink>}
    >
      <PanelBody state={state} minHeight={260}>
        {(sessions) => {
          const week = sessions.filter((session) => Date.parse(session.windowEnd) >= now - WEEK_MS);
          const summary = summarizeIncidents(week, now);
          const sub: Record<IncidentGroup, string> = {
            ongoing: 'a signal in the last 15 min',
            recent: 'quiet for 15 min or more',
            earlier: summary.topService
              ? `most this week from ${summary.topService}`
              : 'more than a day ago',
          };
          return (
            <div className="flex flex-col items-center gap-4">
              <Donut
                size={160}
                label={`${summary.total} incidents: ${summary.counts.ongoing} ongoing, ${summary.counts.recent} ended in the last 24 hours, ${summary.counts.earlier} earlier`}
                parts={rows
                  .filter((row) => summary.counts[row.key] > 0)
                  .map((row) => ({
                    key: row.key,
                    value: summary.counts[row.key],
                    color: row.color,
                  }))}
                center={summary.total.toLocaleString()}
                centerSub="this week"
              />
              <div className="flex w-full flex-col gap-2.5">
                {rows.map((row) => (
                  <span key={row.key} className="flex items-center gap-3">
                    <span
                      className="flex h-8 w-8 shrink-0 items-center justify-center rounded-lg"
                      style={{
                        color: row.color,
                        background: `color-mix(in srgb, ${row.color} 15%, transparent)`,
                      }}
                    >
                      {row.icon}
                    </span>
                    <span className="flex min-w-0 flex-1 flex-col">
                      <span className="text-sm text-gray-800 dark:text-gray-100">{row.label}</span>
                      <span className="truncate text-xs text-gray-500 dark:text-gray-400">
                        {sub[row.key]}
                      </span>
                    </span>
                    <strong className="text-base font-semibold text-gray-900 dark:text-gray-50">
                      {summary.counts[row.key].toLocaleString()}
                    </strong>
                  </span>
                ))}
              </div>
              {summary.total > 0 && (
                <span className="text-xs text-gray-500 dark:text-gray-400">
                  Median length, first to last signal: {formatMinutes(summary.medianMinutes)}
                </span>
              )}
            </div>
          );
        }}
      </PanelBody>
    </Card>
  );
};
