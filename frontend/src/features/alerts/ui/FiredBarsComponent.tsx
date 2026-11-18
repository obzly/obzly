import dayjs from 'dayjs';

import type { ActivityDay } from '../lib/activity';

interface Props {
  days: ActivityDay[];
  /** The largest daily count across the table, so bars compare between rules. */
  scaleMax: number;
  paused: boolean;
}

const BAR_HEIGHT = 22;

const dayLabel = (day: ActivityDay): string =>
  `${dayjs(day.day).format('MMM D')}: ${
    day.fired === null
      ? 'not counted yet'
      : `fired ${day.fired}, ${day.notifications ?? 0} notifications`
  }`;

/** A rule's firings per day as small bars, with the week's total beside them. */
export const FiredBarsComponent = ({ days, scaleMax, paused }: Props) => {
  const known = days.filter((d) => d.fired !== null);
  const total = known.reduce((sum, d) => sum + (d.fired ?? 0), 0);
  const label =
    known.length === 0
      ? 'No data for the last 7 days'
      : `Fired ${total} times in the last ${known.length === days.length ? 7 : known.length} days`;
  return (
    <span className="flex items-end gap-2">
      <span
        role="img"
        aria-label={label}
        title={days.map(dayLabel).join('\n')}
        className="flex w-[84px] items-end gap-0.5"
        style={{ height: BAR_HEIGHT }}
      >
        {days.map((day) =>
          day.fired === null ? (
            <span
              key={day.day}
              className="h-0.5 flex-1 rounded-sm border border-dashed border-gray-300 dark:border-gray-600"
            />
          ) : (
            <span
              key={day.day}
              className="flex-1 rounded-sm"
              style={{
                height: Math.max(2, Math.round((day.fired / Math.max(scaleMax, 1)) * BAR_HEIGHT)),
                background:
                  day.fired === 0
                    ? 'var(--v2-track)'
                    : paused
                      ? 'var(--v2-muted)'
                      : 'var(--v2-orange)',
              }}
            />
          ),
        )}
      </span>
      <span className="text-xs text-gray-500 tabular-nums dark:text-gray-400">
        {known.length === 0 ? 'n/a' : total.toLocaleString()}
      </span>
    </span>
  );
};
