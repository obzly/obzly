import { LoadingOutlined } from '@ant-design/icons';
import { App, Button, Select, Spin } from 'antd';
import React, { useMemo } from 'react';

import type { QueryNode } from '../../../../entity/query';
import { useTheme } from '../../../../shared/theme';
import { formatInTimezone, getTimezoneLabel, useDisplayTimezone } from '../../../../shared/time';
import { computeAutoBucketMs } from '../../../dashboards';
import {
  WEEKDAYS,
  bandClauses,
  bandFindings,
  bandLabel,
  bandMatrix,
  foldWeekdayHour,
  formatInUnit,
  heatmapBucketMs,
  latestSlot,
  timeGrid,
  unitForField,
  weekdayHourPeak,
} from '../../lib/logsChart';
import type { TimeRange } from '../TimeRangePickerComponent';
import { ToggleGroupComponent } from '../ToggleGroupComponent';
import { type PanelQuery, usePanelData } from './usePanelData';

/** Narrow the search to a time slot (and optional extra clauses), then show its lines. */
export interface ChartFilter {
  from: number;
  to: number;
  clauses?: string[];
}

interface Props {
  projectId: string;
  query: QueryNode | null;
  timeRange: TimeRange;
  refreshToken: number;
  mode: 'when' | 'dist';
  onModeChange: (mode: 'when' | 'dist') => void;
  numericFields: string[];
  promotedNumbers: ReadonlySet<string>;
  field?: string;
  onFieldChange: (field: string) => void;
  onApplyFilter: (filter: ChartFilter) => void;
  onSetRange: (from: number, to: number) => void;
}

const DAY_MS = 86_400_000;
// The backend caps an aggregation at 10,000 rows: 36 columns × a few dozen bands fits.
const BAND_ROW_LIMIT = 10_000;
const DIST_COLUMNS = 36;

const describeBucket = (ms: number): string => {
  if (ms % DAY_MS === 0) return ms === DAY_MS ? 'a day' : `${ms / DAY_MS} days`;
  if (ms % 3_600_000 === 0) return ms === 3_600_000 ? 'an hour' : `${ms / 3_600_000} hours`;
  if (ms % 60_000 === 0) return ms === 60_000 ? 'a minute' : `${ms / 60_000} minutes`;
  return `${Math.round(ms / 1000)} seconds`;
};

/** Logs › Chart › Heatmap: when matching lines happen (weekday × hour), or how a number spreads over time. */
export const LogsHeatmapComponent = ({
  projectId,
  query,
  timeRange,
  refreshToken,
  mode,
  onModeChange,
  numericFields,
  promotedNumbers,
  field,
  onFieldChange,
  onApplyFilter,
  onSetRange,
}: Props): React.JSX.Element => {
  const { message } = App.useApp();
  const { definition } = useTheme();
  const [timezone] = useDisplayTimezone();
  const fromMs = timeRange.from.valueOf();
  const toMs = timeRange.to.valueOf();
  const whenBucketMs = heatmapBucketMs(toMs, timezone);
  const distBucketMs = computeAutoBucketMs(fromMs, toMs, DIST_COLUMNS);

  const whenQuery: PanelQuery | null =
    mode === 'when'
      ? {
          panel: { type: 'line', query, bucketMs: whenBucketMs, limit: BAND_ROW_LIMIT },
          from: fromMs,
          to: toMs,
        }
      : null;
  const distQuery: PanelQuery | null =
    mode === 'dist' && field
      ? {
          panel: {
            type: 'line',
            query,
            bucketMs: distBucketMs,
            groupField: field,
            groupBands: 'log',
            limit: BAND_ROW_LIMIT,
          },
          from: fromMs,
          to: toMs,
        }
      : null;
  const [whenData, distData] = usePanelData(projectId, [whenQuery, distQuery], refreshToken);

  const week = useMemo(
    () => (whenData.buckets ? foldWeekdayHour(whenData.buckets, timezone) : null),
    [whenData.buckets, timezone],
  );
  const columns = useMemo(() => timeGrid(fromMs, toMs, distBucketMs), [fromMs, toMs, distBucketMs]);
  const matrix = useMemo(
    () => (distData.buckets ? bandMatrix(distData.buckets, columns) : null),
    [distData.buckets, columns],
  );

  const accent = definition.colors.accent;
  const low = definition.colors.panel2;
  const shade = (value: number, max: number) =>
    value <= 0 || max <= 0
      ? low
      : `color-mix(in srgb, ${accent} ${Math.round(12 + 88 * (value / max))}%, ${low})`;
  const hh = (hour: number) => String(hour).padStart(2, '0');
  const fmtTime = (ms: number) =>
    formatInTimezone(ms, toMs - fromMs > DAY_MS ? 'MMM D, HH:mm' : 'HH:mm', timezone);

  const pickWeekdayHour = (weekday: number, hour: number) => {
    const slot = latestSlot(weekday, hour, fromMs, toMs, timezone);
    if (!slot) {
      message.info(`No ${WEEKDAYS[weekday]} ${hh(hour)}:00 in this range`);
      return;
    }
    onApplyFilter(slot);
  };

  const pickBand = (band: number, column: number) => {
    if (!field) return;
    const promoted = promotedNumbers.has(field);
    if (!promoted) {
      message.info(
        `Narrowed to that time slot; promote ${field} as a number (Project settings) to filter by its band too`,
      );
    }
    onApplyFilter({
      from: column,
      to: Math.min(column + distBucketMs, toMs),
      clauses: promoted ? bandClauses(field, band) : undefined,
    });
  };

  const header = (
    <div className="flex flex-wrap items-start justify-between gap-2">
      <div className="min-w-0">
        <h3 className="m-0 text-[14px] font-semibold text-gray-900 dark:text-gray-50">
          {mode === 'when'
            ? 'When does it happen?'
            : `How ${field ?? 'a number'} is spread over time`}
        </h3>
        <p className="m-0 mt-0.5 text-xs text-gray-500 dark:text-gray-400">
          {mode === 'when'
            ? `Matching lines by weekday and hour (${getTimezoneLabel(timezone)})`
            : `Each column is ${describeBucket(distBucketMs)}; rows are log-scale bands; darker is more lines`}
        </p>
      </div>
      <span className="flex flex-wrap items-center gap-2">
        <ToggleGroupComponent
          label="Heatmap kind"
          size="small"
          options={[
            { value: 'when' as const, label: 'When (weekday × hour)', pressed: mode === 'when' },
            { value: 'dist' as const, label: 'Distribution over time', pressed: mode === 'dist' },
          ]}
          onToggle={onModeChange}
        />
        {mode === 'dist' && (
          <Select
            size="small"
            showSearch
            aria-label="Field"
            placeholder="Pick a number field"
            className="min-w-[160px] font-mono"
            value={field}
            onChange={onFieldChange}
            options={numericFields.map((name) => ({
              value: name,
              label: promotedNumbers.has(name) ? `${name} (number)` : name,
            }))}
          />
        )}
      </span>
    </div>
  );

  const loading = (
    <div className="flex h-40 items-center justify-center">
      <Spin indicator={<LoadingOutlined spin />} />
    </div>
  );
  const problem = (text: string) => (
    <div className="flex min-h-24 items-center justify-center px-2 text-center text-sm text-gray-500 dark:text-gray-400">
      {text}
    </div>
  );

  let body: React.ReactNode;
  let finding: string | null = null;
  let max = 0;
  if (mode === 'when') {
    if (whenData.error) body = problem(whenData.error);
    else if (!week) body = loading;
    else {
      max = Math.max(...week.flat());
      const peak = weekdayHourPeak(week);
      if (!peak) finding = 'No matching lines in this range.';
      else if ((peak.timesAverage ?? 0) < 1.5) {
        finding = `Lines are spread evenly over the week (busiest: ${WEEKDAYS[peak.weekday]} ${hh(peak.hour)}:00).`;
      } else {
        finding = `Most lines on ${WEEKDAYS[peak.weekday]} at ${hh(peak.hour)}:00: ${peak.value.toLocaleString('en-US')}, ${(peak.timesAverage ?? 0).toFixed(1)}× the hourly average.`;
      }
      body = (
        <div className="overflow-x-auto">
          <div
            className="grid min-w-[640px] gap-[3px] text-[11px]"
            style={{ gridTemplateColumns: '40px repeat(24, minmax(0, 1fr))' }}
          >
            <span />
            {Array.from({ length: 24 }, (_, h) => (
              <span key={h} className="text-center text-gray-500 dark:text-gray-400">
                {h % 3 === 0 ? hh(h) : ''}
              </span>
            ))}
            {WEEKDAYS.map((day, d) => (
              <React.Fragment key={day}>
                <span className="text-gray-500 dark:text-gray-400">{day}</span>
                {week[d].map((value, h) => {
                  const title = `${day} ${hh(h)}:00 · ${value.toLocaleString('en-US')} lines`;
                  return (
                    <button
                      key={h}
                      type="button"
                      title={title}
                      aria-label={title}
                      onClick={() => pickWeekdayHour(d, h)}
                      className="focus-visible:ring-accent-500 h-[26px] cursor-pointer rounded border-0 p-0 focus-visible:ring-2 focus-visible:outline-none"
                      style={{ background: shade(value, max) }}
                    />
                  );
                })}
              </React.Fragment>
            ))}
          </div>
        </div>
      );
    }
  } else if (!field) {
    body = problem(
      'No number fields found in these logs; a number field is needed for its distribution',
    );
  } else if (distData.error) {
    body = problem(distData.error);
  } else if (!matrix) {
    body = loading;
  } else if (matrix.bands.length === 0) {
    body = problem(`No lines with a number in ${field} in this range`);
  } else {
    max = matrix.max;
    const unit = unitForField(field);
    const findings = bandFindings(matrix);
    if (findings.newBand !== null && findings.dominant !== null && findings.newSince !== null) {
      finding = `Since ${fmtTime(findings.newSince)} a band at ${bandLabel(findings.newBand, field)} appears next to the usual ${bandLabel(findings.dominant, field)}: an average would show this as a mild rise.`;
    } else if (findings.dominant !== null) {
      finding = `Most lines fall in ${bandLabel(findings.dominant, field)}; no new band.`;
    }
    const every = matrix.bands.length > 12 ? 2 : 1;
    const ticks = [0, 0.25, 0.5, 0.75, 1].map(
      (f) => columns[Math.min(columns.length - 1, Math.round(f * (columns.length - 1)))],
    );
    body = (
      <>
        <div className="flex gap-2">
          <div className="flex flex-col gap-[2px] text-right font-mono text-[11px] text-gray-500 dark:text-gray-400">
            {matrix.bands.map((band, i) => (
              <span key={band} className="flex h-[22px] items-center justify-end">
                {i % every === 0 ? (band <= 0 ? '≤ 0' : formatInUnit(band, unit)) : ''}
              </span>
            ))}
          </div>
          <div className="min-w-0 flex-1 overflow-x-auto">
            <div
              className="grid min-w-[560px] gap-[2px]"
              style={{
                gridTemplateColumns: `repeat(${columns.length}, minmax(0, 1fr))`,
                gridTemplateRows: `repeat(${matrix.bands.length}, 22px)`,
              }}
            >
              {matrix.bands.map((band, r) =>
                columns.map((column, c) => {
                  const value = matrix.cells[r][c];
                  const title = `${bandLabel(band, field)} · ${fmtTime(column)} · ${value.toLocaleString('en-US')} lines`;
                  return (
                    <button
                      key={`${band}-${column}`}
                      type="button"
                      title={title}
                      aria-label={title}
                      onClick={() => pickBand(band, column)}
                      className="focus-visible:ring-accent-500 cursor-pointer rounded-[3px] border-0 p-0 focus-visible:ring-2 focus-visible:outline-none"
                      style={{ background: shade(value, max) }}
                    />
                  );
                }),
              )}
            </div>
          </div>
        </div>
        <div className="flex justify-between pl-12 text-xs text-gray-500 dark:text-gray-400">
          {ticks.map((t, i) => (
            <span key={i}>{fmtTime(t)}</span>
          ))}
        </div>
      </>
    );
  }

  const rangeDays = (toMs - fromMs) / DAY_MS;

  return (
    <section aria-label="Heatmap" className="flex flex-col gap-3">
      {header}
      {mode === 'when' && rangeDays < 7 && (
        <div className="flex flex-wrap items-center gap-2 rounded-lg bg-gray-50 px-3 py-2 text-xs text-gray-600 dark:bg-gray-900/60 dark:text-gray-300">
          <span>
            This range covers {rangeDays < 1 ? 'less than a day' : `${Math.floor(rangeDays)} days`};
            weekdays and hours outside it stay empty.
          </span>
          <Button size="small" onClick={() => onSetRange(toMs - 7 * DAY_MS, toMs)}>
            Show the last 7 days
          </Button>
        </div>
      )}
      {body}
      <div className="flex flex-wrap items-center justify-between gap-2.5">
        <span className="flex items-center gap-1.5 text-xs text-gray-500 dark:text-gray-400">
          fewer
          <span
            aria-hidden="true"
            className="inline-block h-2.5 w-[120px] rounded-full"
            style={{ background: `linear-gradient(90deg, ${low}, ${accent})` }}
          />
          more lines
        </span>
        {finding && <span className="text-xs text-gray-700 dark:text-gray-200">{finding}</span>}
      </div>
      <span className="text-xs text-gray-500 dark:text-gray-400">
        Click a cell to show its lines.{' '}
        {mode === 'when'
          ? 'Hourly counts come from the per-minute rollups when the search allows, otherwise from raw logs within the scan limit.'
          : 'Bands read raw logs within the scan limit.'}
      </span>
    </section>
  );
};
