import React, { useMemo, useState } from 'react';

import type { SQLQueryResponse } from '../../../../entity/query';
import { useTheme } from '../../../../shared/theme';
import { Donut, RankedList } from '../../../../shared/ui/v2';
import { type SqlChartKind, sqlChartShape, sqlSeries, sqlTopRows } from '../../lib/logsChart';
import { ToggleGroupComponent } from '../ToggleGroupComponent';
import { LogsLineChartComponent } from './LogsLineChartComponent';

interface Props {
  response: SQLQueryResponse;
}

const KIND_LABELS: Record<SqlChartKind, string> = {
  series: 'Time series',
  top: 'Top values',
  donut: 'Donut',
};
// A ranked list or donut of more rows than this stops being readable.
const MAX_ROWS = 20;

const formatNumber = (value: number) => value.toLocaleString('en-US', { maximumFractionDigits: 2 });

/** A SQL result drawn as the chart its shape fits (Lines / Chart on SQL results). */
export const SQLResultsChartComponent = ({ response }: Props): React.JSX.Element => {
  const { chartColors } = useTheme();
  const shape = useMemo(
    () => sqlChartShape(response.columns, response.rows),
    [response.columns, response.rows],
  );
  const [picked, setPicked] = useState<SqlChartKind | null>(null);
  const kind = picked && shape.kinds.includes(picked) ? picked : shape.kinds[0];
  const color = (i: number) => chartColors[i % chartColors.length];

  if (!kind) {
    return (
      <div className="py-6 text-center text-sm text-gray-500 dark:text-gray-400">
        This result has no shape to draw: a time column and numbers draw a time series; a label and
        a number draw top values or a donut.
      </div>
    );
  }

  const top = sqlTopRows(shape, response.rows).slice(0, MAX_ROWS);
  const valueName = shape.valueColumns.length ? response.columns[shape.valueColumns[0]] : '';

  return (
    <div className="flex flex-col gap-3">
      <div className="flex flex-wrap items-center gap-2">
        <span className="text-xs text-gray-500 dark:text-gray-400">Chart</span>
        <ToggleGroupComponent
          label="Chart type"
          size="small"
          options={shape.kinds.map((k) => ({
            value: k,
            label: KIND_LABELS[k],
            pressed: kind === k,
          }))}
          onToggle={setPicked}
        />
      </div>
      {kind === 'series' && (
        <LogsLineChartComponent
          ariaLabel="SQL result over time"
          series={sqlSeries(shape, response.columns, response.rows).map((s, i) => ({
            name: s.key,
            color: color(i),
            points: s.points,
          }))}
          formatValue={formatNumber}
          height={260}
        />
      )}
      {kind === 'top' && (
        <RankedList
          rows={top.map((r, i) => ({
            key: `${r.key}-${i}`,
            label: <span className="font-mono">{r.key || '(none)'}</span>,
            value: r.value,
            display: formatNumber(r.value),
            color: color(i),
          }))}
        />
      )}
      {kind === 'donut' && (
        <div className="flex flex-wrap items-center gap-6">
          <Donut
            size={170}
            label={`${valueName} by ${response.columns[shape.labelColumn ?? 0]}`}
            center={formatNumber(top.reduce((sum, r) => sum + r.value, 0))}
            centerSub={valueName}
            parts={top.map((r, i) => ({ key: `${r.key}-${i}`, value: r.value, color: color(i) }))}
          />
          <div className="flex min-w-[200px] flex-1 flex-col gap-1.5">
            {top.map((r, i) => (
              <span key={`${r.key}-${i}`} className="flex items-center gap-2 text-[13px]">
                <i
                  className="inline-block h-2.5 w-2.5 shrink-0 rounded-sm"
                  style={{ background: color(i) }}
                />
                <span className="flex-1 truncate font-mono text-gray-800 dark:text-gray-100">
                  {r.key || '(none)'}
                </span>
                <strong className="font-semibold text-gray-900 dark:text-gray-50">
                  {formatNumber(r.value)}
                </strong>
              </span>
            ))}
          </div>
        </div>
      )}
      {response.truncated && (
        <span className="text-xs text-gray-500 dark:text-gray-400">
          The result was truncated, so the chart shows only the rows returned.
        </span>
      )}
    </div>
  );
};
