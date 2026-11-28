import { LoadingOutlined, SearchOutlined } from '@ant-design/icons';
import { Alert, App, Button, Input, Popconfirm, Spin } from 'antd';
import React, { useEffect, useMemo, useState } from 'react';

import { alertingApi } from '../../../entity/alerting';
import {
  type FieldValuesResponse,
  type PatternTrend,
  type PatternTrendsResponse,
  type QueryNode,
  queryApi,
} from '../../../entity/query';
import { useTheme } from '../../../shared/theme';
import { formatInTimezone, useDisplayTimezone } from '../../../shared/time';
import { Card, KpiTile, Sparkline } from '../../../shared/ui/v2';
import {
  type PatternFilter,
  andNodes,
  firstSeenText,
  formatCount,
  matchesPatternFilter,
  samplingSuggestion,
  sortTrends,
  stackPatterns,
  trendChangeBadge,
} from '../lib/logInsights';
import { LevelPillComponent } from './LevelPillComponent';
import { SegmentedControl, ShareBar, StackedColumns, TonePill } from './LogsViewTabs';
import { SamplingRuleModalComponent, type SamplingTarget } from './SamplingRuleModalComponent';
import type { TimeRange } from './TimeRangePickerComponent';
import { useInsightRange } from './useInsightRange';

interface Props {
  projectId: string;
  /** The search's query, applied to the patterns (null: every log). */
  query: QueryNode | null;
  searchText: string;
  /** Show (or with exclude, hide) a pattern's logs on the Search tab over range. */
  onPatternFilter: (hash: string, exclude: boolean, range: TimeRange) => void;
}

const RANGES = [
  { value: '1h', label: 'Last hour', hours: 1 },
  { value: '3h', label: 'Last 3 hours', hours: 3 },
  { value: '24h', label: 'Last 24 hours', hours: 24 },
  { value: '7d', label: 'Last 7 days', hours: 24 * 7 },
];
const STACKED_PATTERNS = 6;
const WHERE_FIELDS = ['service_name', 'k8s_pod_name', 'service_version'];
const WHERE_SAMPLE_ROWS = 20_000;
const PATTERN_HASH_FIELD = 'pattern_hash';

const patternNode = (hash: string): QueryNode => ({
  type: 'condition',
  condition: { field: PATTERN_HASH_FIELD, operator: 'equals', value: hash },
});

/** Logs › Patterns: message patterns over time, new / spiking / dropping ones, and one pattern's details. */
export const LogPatternsTabComponent = ({
  projectId,
  query,
  searchText,
  onPatternFilter,
}: Props): React.JSX.Element => {
  const { message } = App.useApp();
  const { chartColors, definition } = useTheme();
  const [timezone] = useDisplayTimezone();
  const { range, control, rangeText } = useInsightRange(RANGES, '3h');
  const [response, setResponse] = useState<PatternTrendsResponse | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [isLoading, setIsLoading] = useState(true);
  const [filter, setFilter] = useState<PatternFilter>('all');
  const [findText, setFindText] = useState('');
  const [selectedHash, setSelectedHash] = useState<string | null>(null);
  const [where, setWhere] = useState<Record<string, FieldValuesResponse | null>>({});
  const [samplingTarget, setSamplingTarget] = useState<SamplingTarget | null>(null);
  const [bytesPerLine, setBytesPerLine] = useState(0);
  const [isCreatingAlert, setIsCreatingAlert] = useState(false);

  const createAlert = async (pattern: PatternTrend) => {
    setIsCreatingAlert(true);
    try {
      await alertingApi.createRule(projectId, {
        name: `Log pattern appears: ${(pattern.pattern || pattern.hash).slice(0, 80)}`,
        enabled: true,
        source: 'logs',
        query: { logs: patternNode(pattern.hash) },
        comparison: '>',
        threshold: 0,
        windowSeconds: 300,
        intervalSeconds: 60,
        forSeconds: 0,
        repeatSeconds: 4 * 3600,
        labels: {},
        summary: `Pattern ${pattern.pattern || pattern.hash} logged again`,
        channelIds: [],
      });
      message.success('Alert rule created - pick its channels in Alerts › Rules');
    } catch (e: unknown) {
      message.error(e instanceof Error ? e.message : 'Failed to create the alert rule');
    } finally {
      setIsCreatingAlert(false);
    }
  };

  const openSampling = (pattern: PatternTrend) => {
    const hours = Math.max((range.to.valueOf() - range.from.valueOf()) / 3_600_000, 1 / 60);
    setSamplingTarget({
      hash: pattern.hash,
      patternText: pattern.pattern,
      service: pattern.services[0] ?? '',
      level: pattern.level,
      perDay: (pattern.count / hours) * 24,
    });
    queryApi
      .getProjectStats(projectId)
      .then((stats) =>
        setBytesPerLine(
          stats.totalLogs > 0 ? (stats.totalSizeMb * 1024 * 1024) / stats.totalLogs : 0,
        ),
      )
      .catch(() => setBytesPerLine(0));
  };

  useEffect(() => {
    const abort = new AbortController();
    setIsLoading(true);
    setError(null);
    queryApi
      .getPatternTrends(
        projectId,
        { query, timeRange: { from: range.from.toISOString(), to: range.to.toISOString() } },
        abort.signal,
      )
      .then((result) => {
        setResponse(result);
        setSelectedHash((prev) =>
          prev && result.patterns.some((p) => p.hash === prev)
            ? prev
            : (sortTrends(result.patterns)[0]?.hash ?? null),
        );
      })
      .catch((e: unknown) => {
        if (!abort.signal.aborted) setError(e instanceof Error ? e.message : String(e));
      })
      .finally(() => {
        if (!abort.signal.aborted) setIsLoading(false);
      });
    return () => abort.abort();
  }, [projectId, query, range]);

  useEffect(() => {
    if (!selectedHash) return;
    const abort = new AbortController();
    setWhere({});
    for (const field of WHERE_FIELDS) {
      queryApi
        .getFieldValues(
          projectId,
          {
            field,
            query: andNodes(query, patternNode(selectedHash)),
            timeRange: { from: range.from.toISOString(), to: range.to.toISOString() },
            limit: 1,
            sampleRows: WHERE_SAMPLE_ROWS,
          },
          abort.signal,
        )
        .then((result) => setWhere((prev) => ({ ...prev, [field]: result })))
        .catch(() => {
          if (!abort.signal.aborted) setWhere((prev) => ({ ...prev, [field]: null }));
        });
    }
    return () => abort.abort();
  }, [projectId, query, range, selectedHash]);

  const sorted = useMemo(() => (response ? sortTrends(response.patterns) : []), [response]);
  const stack = useMemo(
    () => (response ? stackPatterns(response, STACKED_PATTERNS) : []),
    [response],
  );
  const stackColor = (i: number, isOther: boolean) =>
    isOther ? definition.colors.muted : chartColors[i % chartColors.length];
  const rows = sorted.filter(
    (p) =>
      matchesPatternFilter(p, filter) &&
      (!findText ||
        `${p.pattern} ${p.sample} ${p.services.join(' ')}`
          .toLowerCase()
          .includes(findText.toLowerCase())),
  );
  const biggest = Math.max(1, ...sorted.map((p) => p.count));
  const selected = sorted.find((p) => p.hash === selectedHash) ?? null;
  const formatTime = (iso: string) => formatInTimezone(new Date(iso).valueOf(), 'HH:mm', timezone);
  const axisLabels = response?.buckets.length
    ? [0, 1, 2, 3].map((i) => {
        const index = Math.round((i * (response.buckets.length - 1)) / 3);
        return formatInTimezone(
          new Date(response.buckets[index]).valueOf(),
          response.bucketMs >= 86_400_000 ? 'MMM D' : 'MMM D HH:mm',
          timezone,
        );
      })
    : [];

  const kpi = (
    label: string,
    value: number,
    tag: string,
    tone: 'bad' | 'warn' | 'info' | 'neutral',
    note: string,
    key: PatternFilter,
  ) => (
    <div className={`rounded-2xl ${filter === key ? 'ring-accent-500 ring-2' : ''}`}>
      <KpiTile
        label={label}
        value={response ? value.toLocaleString() : 'n/a'}
        icon={<TonePill text={tag} tone={tone} />}
        note={note}
        onClick={() => setFilter(key)}
      />
    </div>
  );

  return (
    <div className="flex flex-col gap-4">
      <Card
        title="Patterns"
        subtitle={
          <>
            Message shapes from the per-minute pattern counts · {rangeText}
            {searchText.trim() && (
              <>
                {' '}
                · matching the search <code className="font-mono">{searchText.trim()}</code>
              </>
            )}
          </>
        }
        extra={control}
      >
        {error && <Alert type="error" showIcon message={error} className="mb-3" />}
        {response?.partial && (
          <Alert
            type="info"
            showIcon
            className="mb-3"
            message="More patterns than one query counts: the smallest are left out."
          />
        )}
        {response?.patternVersionSince && (
          <Alert
            type="info"
            showIcon
            className="mb-3"
            message={`Patterns were regrouped on ${formatInTimezone(response.patternVersionSince, 'D MMM YYYY HH:mm', timezone)}: IDs, names, quoted and JSON values and access log paths no longer make a pattern each. Until a week after that, New and Dropping leave out the patterns that changed.`}
          />
        )}
        <section aria-label="Patterns" className="grid grid-cols-2 gap-3 lg:grid-cols-4">
          {kpi(
            'Patterns seen',
            response?.distinct ?? 0,
            'all',
            'neutral',
            'distinct message shapes in the range',
            'all',
          )}
          {kpi(
            'New',
            response?.newCount ?? 0,
            'new',
            'bad',
            'not seen in the previous 7 days',
            'new',
          )}
          {kpi(
            'Spiking',
            response?.spikeCount ?? 0,
            '×2 or more',
            'warn',
            'against the same time yesterday',
            'spiking',
          )}
          {kpi(
            'Dropped',
            response?.dropCount ?? 0,
            '−50% or more',
            'info',
            'possibly a broken flow',
            'dropped',
          )}
        </section>
      </Card>

      <Card
        title="What makes up the volume"
        subtitle={`Top ${STACKED_PATTERNS} patterns per bucket, from the per-minute pattern counts; the rest is "Other"`}
        extra={
          <span className="flex max-w-xl flex-wrap gap-x-3 gap-y-1 text-xs text-gray-500 dark:text-gray-400">
            {stack.map((s, i) => (
              <span key={s.hash ?? 'other'} className="inline-flex max-w-48 items-center gap-1.5">
                <i
                  className="h-2.5 w-2.5 shrink-0 rounded-sm"
                  style={{ background: stackColor(i, s.hash === null) }}
                />
                <span className="truncate font-mono" title={s.label}>
                  {s.label}
                </span>
              </span>
            ))}
          </span>
        }
      >
        {isLoading && !response ? (
          <div className="flex h-[150px] items-center justify-center">
            <Spin indicator={<LoadingOutlined spin />} />
          </div>
        ) : response && response.total > 0 ? (
          <>
            <StackedColumns
              label="Lines per bucket, stacked by pattern"
              columns={response.buckets.map((at, b) => ({
                key: at,
                title: `${formatInTimezone(new Date(at).valueOf(), 'MMM D HH:mm', timezone)}: ${stack
                  .reduce((sum, s) => sum + (s.values[b] ?? 0), 0)
                  .toLocaleString()} lines`,
                segments: stack.map((s, i) => ({
                  value: s.values[b] ?? 0,
                  color: stackColor(i, s.hash === null),
                })),
              }))}
            />
            <div className="mt-1 flex justify-between text-xs text-gray-500 dark:text-gray-400">
              {axisLabels.map((label, i) => (
                <span key={i}>{label}</span>
              ))}
            </div>
          </>
        ) : (
          <div className="py-10 text-center text-sm text-gray-500 dark:text-gray-400">
            No logs in this range
          </div>
        )}
      </Card>

      <div className="flex flex-wrap items-start gap-4">
        <Card
          className="min-w-0 flex-[999_1_640px]"
          title="Patterns"
          subtitle={
            response
              ? `${rows.length} of ${response.distinct.toLocaleString()} patterns · sorted by change, then volume`
              : undefined
          }
          extra={
            <>
              <Input
                size="small"
                allowClear
                prefix={<SearchOutlined />}
                placeholder="Find a pattern"
                aria-label="Find a pattern"
                value={findText}
                onChange={(e) => setFindText(e.target.value)}
                style={{ width: 200 }}
              />
              <SegmentedControl
                label="Show"
                value={filter}
                onChange={setFilter}
                options={[
                  { value: 'all', label: 'All' },
                  { value: 'new', label: 'New' },
                  { value: 'spiking', label: 'Spiking' },
                  { value: 'dropped', label: 'Dropped' },
                ]}
              />
            </>
          }
        >
          <div className="overflow-x-auto">
            <table className="w-full text-sm">
              <thead>
                <tr className="text-left text-xs text-gray-500 dark:text-gray-400">
                  <th className="py-2 pr-3 font-medium">Pattern</th>
                  <th className="min-w-28 py-2 pr-3 font-medium">Trend</th>
                  <th className="py-2 pr-3 text-right font-medium">Lines</th>
                  <th className="min-w-24 py-2 pr-3 font-medium">Share</th>
                  <th className="py-2 pr-3 font-medium">First seen</th>
                  <th className="py-2 font-medium">Change</th>
                </tr>
              </thead>
              <tbody>
                {rows.map((p) => {
                  const badge = trendChangeBadge(p);
                  const isSelected = p.hash === selectedHash;
                  const color = chartColors[sorted.indexOf(p) % chartColors.length];
                  return (
                    <tr
                      key={p.hash}
                      className={`border-t border-gray-100 dark:border-gray-700/60 ${
                        isSelected ? 'bg-accent-500/10' : ''
                      }`}
                    >
                      <td className="max-w-[360px] py-2 pr-3">
                        <button
                          type="button"
                          aria-pressed={isSelected}
                          onClick={() => setSelectedHash(p.hash)}
                          className="flex w-full min-w-0 cursor-pointer flex-col gap-0.5 text-left"
                        >
                          <span
                            className="truncate font-mono text-[12.5px] text-gray-900 dark:text-gray-100"
                            title={p.pattern || p.sample}
                          >
                            {p.pattern || p.sample || p.hash}
                          </span>
                          <span className="flex items-center gap-1.5 text-xs text-gray-500 dark:text-gray-400">
                            <span className="truncate">
                              {p.services.join(', ') || 'no service'}
                            </span>
                            <LevelPillComponent level={p.level} />
                          </span>
                        </button>
                      </td>
                      <td className="py-2 pr-3">
                        <Sparkline
                          values={p.series}
                          color={color}
                          height={24}
                          label={`${p.pattern} trend`}
                        />
                      </td>
                      <td className="py-2 pr-3 text-right font-mono">{p.count.toLocaleString()}</td>
                      <td className="py-2 pr-3">
                        <span className="flex items-center gap-1.5">
                          <ShareBar share={p.count / biggest} color={color} />
                          <span className="text-xs whitespace-nowrap text-gray-500 dark:text-gray-400">
                            {response && response.total > 0
                              ? `${((p.count / response.total) * 100).toFixed(1)}%`
                              : 'n/a'}
                          </span>
                        </span>
                      </td>
                      <td className="py-2 pr-3 text-xs whitespace-nowrap text-gray-500 dark:text-gray-400">
                        {response ? firstSeenText(p, response.buckets, formatTime) : 'n/a'}
                      </td>
                      <td className="py-2">
                        <TonePill text={badge.text} tone={badge.tone} />
                      </td>
                    </tr>
                  );
                })}
                {response && rows.length === 0 && (
                  <tr>
                    <td
                      colSpan={6}
                      className="py-8 text-center text-sm text-gray-500 dark:text-gray-400"
                    >
                      No patterns match
                    </td>
                  </tr>
                )}
              </tbody>
            </table>
          </div>
        </Card>

        <Card
          className="min-w-0 flex-[1_1_340px]"
          title="Pattern"
          subtitle={
            selected ? (
              <span className="font-mono break-all">{selected.pattern || selected.hash}</span>
            ) : (
              'Pick a pattern'
            )
          }
          extra={
            selected && (
              <TonePill
                text={trendChangeBadge(selected).text}
                tone={trendChangeBadge(selected).tone}
              />
            )
          }
        >
          {selected && (
            <div className="flex flex-col gap-3">
              <div className="grid grid-cols-3 gap-2 text-sm">
                <span className="flex flex-col">
                  <span className="text-xs text-gray-500 dark:text-gray-400">Lines</span>
                  <b className="font-semibold">{selected.count.toLocaleString()}</b>
                </span>
                <span className="flex flex-col">
                  <span className="text-xs text-gray-500 dark:text-gray-400">First seen</span>
                  <b className="font-semibold">
                    {response ? firstSeenText(selected, response.buckets, formatTime) : 'n/a'}
                  </b>
                </span>
                <span className="flex flex-col">
                  <span className="text-xs text-gray-500 dark:text-gray-400">Baseline</span>
                  <b className="font-semibold">
                    {selected.yesterday > 0
                      ? `${selected.yesterday.toLocaleString()} 24 h earlier`
                      : selected.previousWeek > 0
                        ? `${formatCount(selected.previousWeek)} in the 7 days before`
                        : 'not seen before'}
                  </b>
                </span>
              </div>
              <strong className="text-[13px] font-semibold">Sample</strong>
              <span className="rounded-lg bg-gray-50 px-2 py-1.5 font-mono text-xs break-all dark:bg-gray-900/60">
                {selected.sample || 'n/a'}
              </span>
              <strong className="text-[13px] font-semibold">Where it comes from</strong>
              {WHERE_FIELDS.map((field) => {
                const result = where[field];
                const top = result?.values[0];
                const share = top && result.matchedRows > 0 ? top.count / result.matchedRows : 0;
                return (
                  <span key={field} className="flex flex-col gap-1">
                    <span className="flex justify-between gap-2 text-[13px]">
                      <span className="truncate font-mono">
                        {field} = {result === undefined ? '…' : (top?.value ?? 'n/a')}
                      </span>
                      <span className="text-gray-500 dark:text-gray-400">
                        {top ? `${Math.round(share * 100)}%` : ''}
                      </span>
                    </span>
                    <ShareBar share={share} color={definition.colors.accent} />
                  </span>
                );
              })}
              <span className="flex flex-wrap gap-1.5">
                <Button
                  size="small"
                  type="primary"
                  onClick={() => onPatternFilter(selected.hash, false, range)}
                >
                  Show these logs
                </Button>
                <Button size="small" onClick={() => onPatternFilter(selected.hash, true, range)}>
                  Hide this pattern
                </Button>
                <Popconfirm
                  title="Alert when this pattern appears?"
                  description="Creates an alert rule that fires when this pattern logs in a 5-minute window."
                  okText="Create rule"
                  onConfirm={() => createAlert(selected)}
                >
                  <Button size="small" loading={isCreatingAlert}>
                    Alert when it appears
                  </Button>
                </Popconfirm>
                <Button
                  size="small"
                  disabled={samplingSuggestion(selected).keep === null}
                  title={
                    samplingSuggestion(selected).keep === null
                      ? 'Error lines are never sampled'
                      : 'Keep only a share of this pattern at ingest'
                  }
                  onClick={() => openSampling(selected)}
                >
                  Sampling…
                </Button>
              </span>
            </div>
          )}
        </Card>
      </div>

      {samplingTarget && (
        <SamplingRuleModalComponent
          projectId={projectId}
          target={samplingTarget}
          bytesPerLine={bytesPerLine}
          initialKeep={samplingSuggestion(samplingTarget).keep ?? 10}
          onClose={() => setSamplingTarget(null)}
          onSaved={() => setSamplingTarget(null)}
        />
      )}
    </div>
  );
};
