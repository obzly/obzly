import {
  AppstoreOutlined,
  ClockCircleOutlined,
  FileTextOutlined,
  ThunderboltOutlined,
} from '@ant-design/icons';
import React, { type ReactNode } from 'react';

import type { Session } from '../../../entity/correlation';
import { KpiTile } from '../../../shared/ui/v2';
import type { TimeRange } from '../../query';
import { triggerTypeLabel } from './sessionFormat';

interface Props {
  sessions: Session[];
  range: TimeRange;
  limit: number;
}

const TREND_BUCKETS = 14;

const IconTile = ({ color, children }: { color: string; children: ReactNode }) => (
  <span
    aria-hidden="true"
    className="flex h-[34px] w-[34px] shrink-0 items-center justify-center rounded-[10px] text-base"
    style={{ background: `color-mix(in srgb, ${color} 14%, transparent)`, color }}
  >
    {children}
  </span>
);

// Sessions per slice of the searched range, by creation time.
const countPerBucket = (sessions: Session[], range: TimeRange): number[] => {
  const from = range.from.valueOf();
  const span = Math.max(range.to.valueOf() - from, 1);
  const counts = new Array<number>(TREND_BUCKETS).fill(0);
  for (const session of sessions) {
    const index = Math.floor(
      ((new Date(session.createdAt).getTime() - from) / span) * TREND_BUCKETS,
    );
    counts[Math.min(Math.max(index, 0), TREND_BUCKETS - 1)] += 1;
  }
  return counts;
};

const formatDuration = (ms: number): string => {
  const seconds = Math.round(ms / 1000);
  if (seconds < 60) return `${seconds} s`;
  const minutes = Math.round(seconds / 60);
  if (minutes < 60) return `${minutes} min`;
  const hours = Math.floor(minutes / 60);
  const rest = minutes % 60;
  return rest ? `${hours} h ${rest} min` : `${hours} h`;
};

/** Headline numbers computed from the sessions the list already loaded; nothing extra is fetched. */
export const SessionsKpiTiles = ({ sessions, range, limit }: Props): React.JSX.Element => {
  const byService = new Map<string, number>();
  const byTrigger = new Map<string, number>();
  for (const session of sessions) {
    byService.set(session.serviceName, (byService.get(session.serviceName) ?? 0) + 1);
    byTrigger.set(session.triggerType, (byTrigger.get(session.triggerType) ?? 0) + 1);
  }

  const topService = [...byService.entries()].sort((a, b) => b[1] - a[1])[0];
  const errorLogSessions = sessions.filter((s) => s.triggerType === 'error_log');
  const otherTriggers = [...byTrigger.entries()]
    .filter(([type]) => type !== 'error_log')
    .map(([type, count]) => `${triggerTypeLabel(type).toLowerCase()} ${count}`)
    .join(' · ');

  const longest = sessions.reduce<{ session: Session; ms: number } | null>((best, session) => {
    const ms = new Date(session.windowEnd).getTime() - new Date(session.windowStart).getTime();
    return !best || ms > best.ms ? { session, ms } : best;
  }, null);

  const atLimit = sessions.length >= limit;

  return (
    <section
      aria-label="Key numbers"
      className="grid [grid-template-columns:repeat(auto-fit,minmax(220px,1fr))] gap-4"
    >
      <KpiTile
        label="Sessions"
        value={atLimit ? `${sessions.length}+` : sessions.length}
        note={atLimit ? `the list stops at ${limit}` : 'in the selected range'}
        trend={countPerBucket(sessions, range)}
        color="var(--v2-red)"
        icon={
          <IconTile color="var(--v2-red)">
            <ThunderboltOutlined />
          </IconTile>
        }
      />
      <KpiTile
        label="Services affected"
        value={byService.size}
        note={
          topService
            ? topService[1] > 1
              ? `${topService[0]} appears in ${topService[1]} sessions`
              : 'one session each'
            : 'no sessions'
        }
        color="var(--v2-orange)"
        icon={
          <IconTile color="var(--v2-orange)">
            <AppstoreOutlined />
          </IconTile>
        }
      />
      <KpiTile
        label="Error log triggers"
        value={errorLogSessions.length}
        note={otherTriggers || 'no other trigger types'}
        trend={countPerBucket(errorLogSessions, range)}
        color="var(--v2-blue)"
        icon={
          <IconTile color="var(--v2-blue)">
            <FileTextOutlined />
          </IconTile>
        }
      />
      <KpiTile
        label="Longest window"
        value={longest ? formatDuration(longest.ms) : 'n/a'}
        note={longest ? longest.session.serviceName : 'no sessions'}
        color="var(--v2-accent)"
        icon={
          <IconTile color="var(--v2-accent)">
            <ClockCircleOutlined />
          </IconTile>
        }
      />
    </section>
  );
};
