import { LoadingOutlined } from '@ant-design/icons';
import { App, Button, Select, Spin, Tooltip } from 'antd';
import React, { useEffect, useMemo, useState } from 'react';

import type { RuleRequest } from '../../../../entity/alerting';
import type { LogMeasure, Panel } from '../../../../entity/dashboards';
import { projectApi } from '../../../../entity/projects';
import type { QueryNode, QueryableField } from '../../../../entity/query';
import { useTheme } from '../../../../shared/theme';
import { formatInTimezone, useDisplayTimezone } from '../../../../shared/time';
import { Card, Donut, RankedList } from '../../../../shared/ui/v2';
import { computeAutoBucketMs } from '../../../dashboards';
import {
  type ChartKind,
  type CompareKind,
  KIND_NAMES,
  type MeasureChoice,
  PANEL_TYPE_FOR_KIND,
  andQuery,
  chartSql,
  defaultNumericField,
  donutFits,
  firstSeen,
  formatMeasureValue,
  inCondition,
  isAdditiveMeasure,
  measureLabel,
  rankNumericFields,
  seriesOnGrid,
  timeGrid,
  topRows,
  usesRollups,
} from '../../lib/logsChart';
import type { TimeRange } from '../TimeRangePickerComponent';
import { ToggleGroupComponent } from '../ToggleGroupComponent';
import { LogsCompareComponent } from './LogsCompareComponent';
import { type ChartFilter, LogsHeatmapComponent } from './LogsHeatmapComponent';
import { LogsLineChartComponent } from './LogsLineChartComponent';
import { SaveToDashboardComponent } from './SaveToDashboardComponent';
import { type PanelQuery, usePanelData } from './usePanelData';

interface Props {
  projectId: string;
  /** The executed search (hidden levels included) and its range. */
  query: QueryNode | null;
  timeRange: TimeRange;
  refreshToken: number;
  fields: QueryableField[];
  /** The Lines / Chart switch. */
  viewSwitch: React.ReactNode;
  /** "24,690 matching lines", when known. */
  totalText?: string;
  /** A heatmap cell: narrow the search to it and show its lines. */
  onApplyFilter: (filter: ChartFilter) => void;
  /** Widen the search range (the weekday heatmap wants a week). */
  onSetRange: (from: number, to: number) => void;
  onFilterPattern: (hash: string) => void;
  /** Opens Alerts › New rule with this draft; omit to hide "Create alert". */
  onCreateAlert?: (draft: Partial<RuleRequest>) => void;
}

interface ChartSettings {
  kind: ChartKind;
  measure: MeasureChoice;
  measureField?: string;
  split: string;
  top: 5 | 10;
  heatmapMode: 'when' | 'dist';
  bandField?: string;
  compare: CompareKind;
}

const STORAGE_KEY = 'obzly-logs-chart-v1';
const DEFAULTS: ChartSettings = {
  kind: 'series',
  measure: 'count',
  split: 'service_name',
  top: 5,
  heatmapMode: 'when',
  compare: 'prev',
};
const QUICK_SPLITS = ['service_name', 'level', 'k8s_namespace_name', 'k8s_pod_name'];
const KINDS: { value: ChartKind; label: string }[] = [
  { value: 'series', label: 'Time series' },
  { value: 'top', label: 'Top values' },
  { value: 'table', label: 'Table' },
  { value: 'donut', label: 'Donut' },
  { value: 'heatmap', label: 'Heatmap' },
  { value: 'compare', label: 'Compare' },
];
const MEASURES: { value: MeasureChoice; label: string }[] = [
  { value: 'count', label: 'Count lines' },
  { value: 'distinct', label: 'Distinct' },
  { value: 'avg', label: 'Avg' },
  { value: 'p95', label: 'p95' },
];
const ERROR_LEVELS = ['error', 'fatal'];

const readSettings = (): ChartSettings => {
  try {
    const raw = localStorage.getItem(STORAGE_KEY);
    return raw ? { ...DEFAULTS, ...(JSON.parse(raw) as Partial<ChartSettings>) } : DEFAULTS;
  } catch {
    return DEFAULTS;
  }
};

const writeSettings = (settings: ChartSettings) => {
  try {
    localStorage.setItem(STORAGE_KEY, JSON.stringify(settings));
  } catch {
    // A per-browser convenience only.
  }
};

const ControlField = ({ label, children }: { label: string; children: React.ReactNode }) => (
  <div className="flex flex-col gap-1">
    <span className="text-xs text-gray-500 dark:text-gray-400">{label}</span>
    {children}
  </div>
);

const Loading = () => (
  <div className="flex h-40 items-center justify-center">
    <Spin indicator={<LoadingOutlined spin />} />
  </div>
);

const Problem = ({ text }: { text: string }) => (
  <div className="flex min-h-24 items-center justify-center px-2 text-center text-sm text-gray-500 dark:text-gray-400">
    {text}
  </div>
);

/** Logs › Chart: the search drawn as a time series, top values, a table, a donut, a heatmap or a comparison. */
export const LogsChartViewComponent = ({
  projectId,
  query,
  timeRange,
  refreshToken,
  fields,
  viewSwitch,
  totalText,
  onApplyFilter,
  onSetRange,
  onFilterPattern,
  onCreateAlert,
}: Props): React.JSX.Element => {
  const { message } = App.useApp();
  const { chartColors, status } = useTheme();
  const [timezone] = useDisplayTimezone();
  const [settings, setSettings] = useState<ChartSettings>(readSettings);
  const [promotedNumbers, setPromotedNumbers] = useState<ReadonlySet<string>>(new Set());

  const update = (patch: Partial<ChartSettings>) =>
    setSettings((prev) => {
      const next = { ...prev, ...patch };
      writeSettings(next);
      return next;
    });

  useEffect(() => {
    let cancelled = false;
    projectApi
      .getPromotedFields(projectId)
      .then((promoted) => {
        if (!cancelled) {
          setPromotedNumbers(
            new Set(promoted.filter((p) => p.slot.startsWith('n')).map((p) => p.field)),
          );
        }
      })
      .catch(() => {
        // Without it, number fields are guessed from their names.
      });
    return () => {
      cancelled = true;
    };
  }, [projectId]);

  const fieldNames = useMemo(() => fields.map((f) => f.name), [fields]);
  const customNames = useMemo(() => fields.filter((f) => f.isCustom).map((f) => f.name), [fields]);
  const numericFields = useMemo(
    () => rankNumericFields(customNames, promotedNumbers),
    [customNames, promotedNumbers],
  );

  const { kind, measure, split, top } = settings;
  const measureField =
    measure === 'count'
      ? undefined
      : measure === 'distinct'
        ? (settings.measureField ?? 'trace_id')
        : (settings.measureField ?? defaultNumericField(customNames, promotedNumbers));
  const effectiveMeasure: LogMeasure = measure !== 'count' && !measureField ? 'count' : measure;
  const additive = isAdditiveMeasure(effectiveMeasure);
  const label = measureLabel(effectiveMeasure, measureField);
  const fromMs = timeRange.from.valueOf();
  const toMs = timeRange.to.valueOf();
  const bucketMs = computeAutoBucketMs(fromMs, toMs, 60);
  const needsSplit = kind === 'top' || kind === 'table' || kind === 'donut';
  const panelKind = PANEL_TYPE_FOR_KIND[kind];
  const measurePart =
    effectiveMeasure === 'count' ? {} : { measure: effectiveMeasure, measureField };

  // ---- queries ----
  const isBasic = kind === 'series' || needsSplit;
  const topQuery: PanelQuery | null =
    isBasic && split && kind !== 'series'
      ? {
          panel: { type: 'rankedList', query, groupField: split, limit: top, ...measurePart },
          from: fromMs,
          to: toMs,
        }
      : null;
  const seriesQuery: PanelQuery | null =
    kind === 'series'
      ? {
          panel: {
            type: 'line',
            query,
            bucketMs,
            ...(split ? { groupField: split, topGroups: top } : {}),
            ...measurePart,
          },
          from: fromMs,
          to: toMs,
        }
      : null;
  const [topData, seriesData] = usePanelData(projectId, [topQuery, seriesQuery], refreshToken);
  const rows = useMemo(() => topRows(topData.buckets ?? [], additive), [topData.buckets, additive]);
  const keys = rows.map((r) => r.key).filter((k) => k !== '');

  // The table's error lines and first-seen time, for the top groups only.
  const tableExtras: (PanelQuery | null)[] =
    kind === 'table' && split && keys.length > 0
      ? [
          {
            panel: {
              type: 'table',
              groupField: split,
              limit: top,
              query: andQuery(query, inCondition(split, keys), inCondition('level', ERROR_LEVELS)),
            },
            from: fromMs,
            to: toMs,
          },
          {
            panel: {
              type: 'line',
              groupField: split,
              bucketMs,
              query: andQuery(query, inCondition(split, keys)),
            },
            from: fromMs,
            to: toMs,
          },
        ]
      : [null, null];
  const [errorData, firstData] = usePanelData(projectId, tableExtras, refreshToken);

  // ---- actions ----
  const panelForSave: Omit<Panel, 'id' | 'gridPos'> | null = panelKind
    ? {
        title: split ? `${label} by ${split}` : label,
        type: panelKind,
        query,
        ...(split ? { groupField: split } : {}),
        ...(panelKind === 'line' ? (split ? { topGroups: top } : {}) : { limit: top }),
        ...measurePart,
      }
    : null;

  const copySql = async () => {
    const sql = chartSql({
      query,
      kind,
      measure: effectiveMeasure,
      measureField,
      split: kind === 'heatmap' ? '' : split,
      top,
      bucketMs,
    });
    if (sql === null) {
      message.warning("This search filters on a message pattern, which SQL mode can't express");
      return;
    }
    try {
      await navigator.clipboard.writeText(sql);
      message.success('SQL copied');
    } catch {
      message.error('Could not copy to the clipboard');
    }
  };

  const createAlert = () => {
    if (!onCreateAlert) return;
    if (effectiveMeasure !== 'count') {
      message.info(
        `Log alerts count lines, so the rule counts this search rather than the ${label}`,
      );
    }
    onCreateAlert({
      name: split ? `Matching lines by ${split}` : 'Matching lines',
      source: 'logs',
      query: { logs: query ?? undefined, groupBy: split || undefined },
      comparison: '>',
      windowSeconds: 300,
    });
  };

  // ---- views ----
  const color = (i: number) => chartColors[i % chartColors.length];
  const format = (value: number | null) =>
    formatMeasureValue(value, effectiveMeasure, measureField);
  const fmtTime = (ms: number) =>
    formatInTimezone(ms, toMs - fromMs > 86_400_000 ? 'MMM D, HH:mm' : 'HH:mm', timezone);

  const renderSeries = () => {
    if (seriesData.error) return <Problem text={seriesData.error} />;
    if (!seriesData.buckets) return <Loading />;
    const grid = timeGrid(fromMs, toMs, bucketMs);
    const series = seriesOnGrid(seriesData.buckets, grid, additive ? 0 : null);
    if (series.length === 0) return <Problem text="No matching lines in this range" />;
    return (
      <>
        <div className="flex flex-wrap gap-x-4 gap-y-1 text-xs text-gray-600 dark:text-gray-300">
          {series.map((s, i) => (
            <span key={s.key} className="inline-flex items-center gap-1.5">
              <i className="inline-block h-2.5 w-2.5 rounded-sm" style={{ background: color(i) }} />
              <span className="font-mono">{s.key || (split ? '(none)' : label)}</span>
            </span>
          ))}
        </div>
        <LogsLineChartComponent
          ariaLabel={`${label} over time${split ? ` split by ${split}` : ''}`}
          series={series.map((s, i) => ({
            name: s.key || (split ? '(none)' : label),
            color: color(i),
            points: s.values.map((v, j) => [grid[j], v]),
          }))}
          formatValue={(v) => format(v)}
        />
      </>
    );
  };

  const renderTop = () => {
    if (topData.error) return <Problem text={topData.error} />;
    if (!topData.buckets) return <Loading />;
    return (
      <RankedList
        empty="No matching lines in this range"
        rows={rows.map((r, i) => ({
          key: r.key,
          label: <span className="font-mono">{r.key || '(none)'}</span>,
          value: r.value,
          display: format(r.value),
          extra: r.share !== null ? `${(r.share * 100).toFixed(1)}%` : undefined,
          color: color(i),
        }))}
      />
    );
  };

  const renderDonut = () => {
    if (!donutFits(effectiveMeasure)) {
      return <Problem text="Donut only fits counts; pick Count lines or Distinct to draw it." />;
    }
    if (topData.error) return <Problem text={topData.error} />;
    if (!topData.buckets) return <Loading />;
    if (rows.length === 0) return <Problem text="No matching lines in this range" />;
    const total = rows.reduce((sum, r) => sum + r.value, 0);
    return (
      <div className="flex flex-wrap items-center gap-6">
        <Donut
          size={170}
          label={`${label} by ${split}`}
          center={format(total)}
          centerSub={label}
          parts={rows.map((r, i) => ({ key: r.key, value: r.value, color: color(i) }))}
        />
        <div className="flex min-w-[200px] flex-1 flex-col gap-1.5">
          {rows.map((r, i) => (
            <span key={r.key} className="flex items-center gap-2 text-[13px]">
              <i
                className="inline-block h-2.5 w-2.5 shrink-0 rounded-sm"
                style={{ background: color(i) }}
              />
              <span className="flex-1 truncate font-mono text-gray-800 dark:text-gray-100">
                {r.key || '(none)'}
              </span>
              <strong className="font-semibold text-gray-900 dark:text-gray-50">
                {format(r.value)}
              </strong>
              <span className="w-12 text-right text-gray-500 dark:text-gray-400">
                {total > 0 ? `${((r.value / total) * 100).toFixed(1)}%` : 'n/a'}
              </span>
            </span>
          ))}
        </div>
      </div>
    );
  };

  const renderTable = () => {
    if (topData.error) return <Problem text={topData.error} />;
    if (!topData.buckets) return <Loading />;
    if (rows.length === 0) return <Problem text="No matching lines in this range" />;
    const errors = new Map(topRows(errorData.buckets ?? [], true).map((r) => [r.key, r.value]));
    const first = firstSeen(firstData.buckets ?? []);
    const cell = 'px-3 py-2 text-right whitespace-nowrap';
    return (
      <div className="overflow-x-auto">
        <table className="w-full border-collapse text-[13px]">
          <thead>
            <tr className="border-b border-gray-200 text-xs text-gray-500 dark:border-gray-700 dark:text-gray-400">
              <th className="px-3 py-2 text-left font-mono font-medium">{split}</th>
              <th className={`${cell} font-medium`}>
                {label.charAt(0).toUpperCase() + label.slice(1)}
              </th>
              {additive && <th className={`${cell} font-medium`}>Share</th>}
              <th className={`${cell} font-medium`}>
                <Tooltip title="Lines at level error or fatal">Errors</Tooltip>
              </th>
              <th className={`${cell} font-medium`}>
                <Tooltip title="The first time bucket the value logged in this range">
                  First seen
                </Tooltip>
              </th>
            </tr>
          </thead>
          <tbody>
            {rows.map((r) => (
              <tr
                key={r.key}
                className="border-b border-gray-100 last:border-0 dark:border-gray-700/60"
              >
                <td className="px-3 py-2 font-mono text-gray-800 dark:text-gray-100">
                  {r.key || '(none)'}
                </td>
                <td className={`${cell} text-gray-900 dark:text-gray-50`}>{format(r.value)}</td>
                {additive && (
                  <td className={`${cell} text-gray-600 dark:text-gray-300`}>
                    {r.share !== null ? `${(r.share * 100).toFixed(1)}%` : 'n/a'}
                  </td>
                )}
                <td className={cell} style={errors.get(r.key) ? { color: status.bad } : undefined}>
                  {errorData.isLoading
                    ? '…'
                    : errorData.error
                      ? 'n/a'
                      : (errors.get(r.key) ?? 0).toLocaleString('en-US')}
                </td>
                <td className={`${cell} text-gray-600 dark:text-gray-300`}>
                  {firstData.isLoading
                    ? '…'
                    : first.has(r.key)
                      ? fmtTime(first.get(r.key) as number)
                      : 'n/a'}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    );
  };

  const subtitle =
    kind === 'heatmap' || kind === 'compare'
      ? totalText
      : [totalText, `${label}${split ? ` split by ${split}` : ''}`].filter(Boolean).join(', ');

  const otherSplitOptions = fieldNames
    .filter((name) => !QUICK_SPLITS.includes(name))
    .map((name) => ({ value: name, label: name }));

  return (
    <Card
      title="Chart of this search"
      subtitle={subtitle}
      extra={
        <>
          {viewSwitch}
          {panelForSave && (
            <SaveToDashboardComponent
              key={JSON.stringify(panelForSave)}
              projectId={projectId}
              panel={panelForSave}
              typeName={KIND_NAMES[kind]}
              disabled={needsSplit && !split}
              disabledReason="Pick a field to split by first"
            />
          )}
          {onCreateAlert && <Button onClick={createAlert}>Create alert</Button>}
          <Tooltip title="Copy the query behind this chart as SQL">
            <Button onClick={copySql}>Copy as SQL</Button>
          </Tooltip>
        </>
      }
      bodyClassName="flex flex-col gap-3"
    >
      <div className="flex flex-wrap items-end gap-3.5 rounded-xl bg-gray-50 p-3 dark:bg-gray-900/60">
        <ControlField label="Chart">
          <ToggleGroupComponent
            label="Chart type"
            size="small"
            options={KINDS.map((k) => ({
              value: k.value,
              label: k.label,
              pressed: kind === k.value,
            }))}
            onToggle={(value) => update({ kind: value })}
          />
        </ControlField>
        {isBasic && (
          <>
            <ControlField label="Measure">
              <span className="flex flex-wrap items-center gap-1.5">
                <ToggleGroupComponent
                  label="Measure"
                  size="small"
                  options={MEASURES.map((m) => ({
                    value: m.value,
                    label: m.label,
                    pressed: measure === m.value,
                  }))}
                  onToggle={(value) => update({ measure: value, measureField: undefined })}
                />
                {measure !== 'count' && (
                  <Select
                    size="small"
                    showSearch
                    aria-label="Measure field"
                    placeholder="Pick a field"
                    className="min-w-[150px]"
                    value={measureField}
                    onChange={(value: string) => update({ measureField: value })}
                    options={(measure === 'distinct' ? fieldNames : numericFields).map((name) => ({
                      value: name,
                      label: name,
                    }))}
                  />
                )}
              </span>
            </ControlField>
            <ControlField label="Split by">
              <span className="flex flex-wrap items-center gap-1.5">
                <ToggleGroupComponent
                  label="Split by"
                  size="small"
                  options={[
                    ...(kind === 'series'
                      ? [{ value: '', label: 'none', pressed: split === '' }]
                      : []),
                    ...QUICK_SPLITS.filter((s) => s === 'level' || fieldNames.includes(s)).map(
                      (s) => ({
                        value: s,
                        label: <span className="font-mono">{s}</span>,
                        pressed: split === s,
                      }),
                    ),
                  ]}
                  onToggle={(value) => update({ split: value })}
                />
                <Select
                  size="small"
                  showSearch
                  allowClear
                  aria-label="Split by another field"
                  placeholder="Other field…"
                  className="min-w-[140px]"
                  value={split && !QUICK_SPLITS.includes(split) ? split : undefined}
                  onChange={(value?: string) => update({ split: value ?? '' })}
                  options={otherSplitOptions}
                />
              </span>
            </ControlField>
            {(split || kind !== 'series') && (
              <ControlField label="Top">
                <ToggleGroupComponent
                  label="Top"
                  size="small"
                  options={([5, 10] as const).map((n) => ({
                    value: n,
                    label: String(n),
                    pressed: top === n,
                  }))}
                  onToggle={(value) => update({ top: value })}
                />
              </ControlField>
            )}
          </>
        )}
        {kind === 'compare' && (
          <ControlField label="Split changes by">
            <span className="flex flex-wrap items-center gap-1.5">
              <ToggleGroupComponent
                label="Split changes by"
                size="small"
                options={['service_name', 'level', 'k8s_pod_name'].map((s) => ({
                  value: s,
                  label: <span className="font-mono">{s}</span>,
                  pressed: (split || 'service_name') === s,
                }))}
                onToggle={(value) => update({ split: value })}
              />
              <Select
                size="small"
                showSearch
                aria-label="Split changes by another field"
                placeholder="Other field…"
                className="min-w-[140px]"
                value={
                  split && !['service_name', 'level', 'k8s_pod_name'].includes(split)
                    ? split
                    : undefined
                }
                onChange={(value: string) => update({ split: value })}
                options={fieldNames.map((name) => ({ value: name, label: name }))}
              />
            </span>
          </ControlField>
        )}
      </div>

      {kind === 'series' && renderSeries()}
      {kind === 'top' && (split ? renderTop() : <Problem text="Pick a field to split by" />)}
      {kind === 'table' && (split ? renderTable() : <Problem text="Pick a field to split by" />)}
      {kind === 'donut' && (split ? renderDonut() : <Problem text="Pick a field to split by" />)}
      {kind === 'heatmap' && (
        <LogsHeatmapComponent
          projectId={projectId}
          query={query}
          timeRange={timeRange}
          refreshToken={refreshToken}
          mode={settings.heatmapMode}
          onModeChange={(mode) => update({ heatmapMode: mode })}
          numericFields={numericFields}
          promotedNumbers={promotedNumbers}
          field={settings.bandField ?? defaultNumericField(customNames, promotedNumbers)}
          onFieldChange={(field) => update({ bandField: field })}
          onApplyFilter={onApplyFilter}
          onSetRange={onSetRange}
        />
      )}
      {kind === 'compare' && (
        <LogsCompareComponent
          projectId={projectId}
          query={query}
          timeRange={timeRange}
          refreshToken={refreshToken}
          split={split || 'service_name'}
          compare={settings.compare}
          onCompareChange={(compare) => update({ compare })}
          onFilterPattern={onFilterPattern}
        />
      )}

      {isBasic && (
        <span className="text-xs text-gray-500 dark:text-gray-400">
          {kind === 'donut' && !donutFits(effectiveMeasure)
            ? 'Donut only fits counts; pick Count lines or Distinct to draw it.'
            : usesRollups(query, split, effectiveMeasure)
              ? 'Same queries as dashboard panels: these counts come from the per-minute rollups.'
              : effectiveMeasure === 'count'
                ? 'Same queries as dashboard panels: these counts read raw logs (the per-minute rollups cover service_name, level, namespace, container, pod and pattern), within the query scan limit.'
                : 'Same queries as dashboard panels: averages, percentiles and distinct counts read raw logs, within the query scan limit.'}
        </span>
      )}
    </Card>
  );
};
