import { LoadingOutlined } from '@ant-design/icons';
import { Alert, Button, Spin } from 'antd';
import React, { useEffect, useMemo, useState } from 'react';

import {
  type HealthMapGroupBy,
  type HealthMapResponse,
  type HealthTile,
  type LogPattern,
  type QueryNode,
  queryApi,
} from '../../../entity/query';
import { useTheme } from '../../../shared/theme';
import { Card } from '../../../shared/ui/v2';
import {
  ERROR_LEVELS,
  type HealthColourBy,
  andNodes,
  formatCount,
  healthColor,
  healthMetric,
  healthScale,
  healthTileFilter,
  levelsNode,
} from '../lib/logInsights';
import { LogExplainPanelComponent } from './LogExplainPanelComponent';
import { SegmentedControl, TonePill } from './LogsViewTabs';
import type { TimeRange } from './TimeRangePickerComponent';
import { useInsightRange } from './useInsightRange';

interface Props {
  projectId: string;
  query: QueryNode | null;
  searchText: string;
  /** Opens the Search tab on these clauses (replacing the search) over range. */
  onOpenLogs: (clauses: string[], range: TimeRange) => void;
  /** Adds "field = value" (exclude: "≠") to the search over range, from the Explain panel. */
  onFilter: (field: string, value: string, exclude: boolean, range: TimeRange) => void;
  onOpenInfrastructure?: () => void;
}

const RANGES = [
  { value: '1h', label: 'Last hour', hours: 1 },
  { value: '6h', label: 'Last 6 hours', hours: 6 },
  { value: '24h', label: 'Last 24 hours', hours: 24 },
  { value: '7d', label: 'Last 7 days', hours: 24 * 7 },
];
const MAX_GROUPS = 10;
const MAX_TILES_PER_GROUP = 10;
const TOP_ERROR_PATTERNS = 3;
const PATTERN_SAMPLE_ROWS = 20_000;
const MAP_TITLES: Record<HealthMapGroupBy, string> = {
  k8s: 'Namespaces and workloads',
  host: 'Hosts and services',
  service: 'Services',
};

/** Logs › Health map: workloads sized by log volume, coloured by error share; click for details. */
export const LogHealthMapTabComponent = ({
  projectId,
  query,
  searchText,
  onOpenLogs,
  onFilter,
  onOpenInfrastructure,
}: Props): React.JSX.Element => {
  const { status, definition } = useTheme();
  const { range, control, rangeText } = useInsightRange(RANGES, '24h');
  const [groupBy, setGroupBy] = useState<HealthMapGroupBy>('k8s');
  const [colourBy, setColourBy] = useState<HealthColourBy>('err');
  const [response, setResponse] = useState<HealthMapResponse | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [isLoading, setIsLoading] = useState(true);
  const [selected, setSelected] = useState<{ group: string; tile: string } | null>(null);
  const [errorPatterns, setErrorPatterns] = useState<LogPattern[] | null>(null);
  const [isExplaining, setIsExplaining] = useState(false);

  useEffect(() => {
    const abort = new AbortController();
    setIsLoading(true);
    setError(null);
    queryApi
      .getHealthMap(
        projectId,
        {
          query,
          groupBy,
          timeRange: { from: range.from.toISOString(), to: range.to.toISOString() },
        },
        abort.signal,
      )
      .then((result) => {
        setResponse(result);
        setSelected((prev) => {
          const keep =
            prev &&
            result.groups.some(
              (g) => g.name === prev.group && g.tiles.some((t) => t.name === prev.tile),
            );
          if (keep) return prev;
          // Start on the tile with the most error lines.
          let best: { group: string; tile: string; errors: number } | null = null;
          for (const g of result.groups) {
            for (const t of g.tiles) {
              if (!best || t.errors > best.errors)
                best = { group: g.name, tile: t.name, errors: t.errors };
            }
          }
          return best ? { group: best.group, tile: best.tile } : null;
        });
      })
      .catch((e: unknown) => {
        if (!abort.signal.aborted) setError(e instanceof Error ? e.message : String(e));
      })
      .finally(() => {
        if (!abort.signal.aborted) setIsLoading(false);
      });
    return () => abort.abort();
  }, [projectId, query, groupBy, range]);

  const selectedGroup = response?.groups.find((g) => g.name === selected?.group);
  const selectedTile = selectedGroup?.tiles.find((t) => t.name === selected?.tile);
  const tileFilter = useMemo(
    () => (selected ? healthTileFilter(groupBy, selected.group, selected.tile) : null),
    [groupBy, selected],
  );

  useEffect(() => {
    setIsExplaining(false);
    if (!tileFilter) return;
    const abort = new AbortController();
    setErrorPatterns(null);
    queryApi
      .getPatterns(
        projectId,
        {
          query: andNodes(query, tileFilter.node, levelsNode(ERROR_LEVELS)),
          timeRange: { from: range.from.toISOString(), to: range.to.toISOString() },
          limit: TOP_ERROR_PATTERNS,
          sampleRows: PATTERN_SAMPLE_ROWS,
        },
        abort.signal,
      )
      .then((result) => setErrorPatterns(result.patterns))
      .catch(() => {
        if (!abort.signal.aborted) setErrorPatterns([]);
      });
    return () => abort.abort();
  }, [projectId, query, range, tileFilter]);

  // Stable, so the Explain panel doesn't refetch on every render.
  const explainRange = useMemo(
    () => ({ from: range.from.valueOf(), to: range.to.valueOf() }),
    [range],
  );
  const explainQuery = useMemo(() => andNodes(query, tileFilter?.node), [query, tileFilter]);

  const groups = (response?.groups ?? []).slice(0, MAX_GROUPS);
  const notShown =
    (response?.hiddenTiles ?? 0) +
    (response?.groups ?? []).reduce(
      (sum, g, i) =>
        sum + (i < MAX_GROUPS ? Math.max(g.tiles.length - MAX_TILES_PER_GROUP, 0) : g.tiles.length),
      0,
    );
  const { warnAt, badAt } = healthScale(colourBy);
  const scaleGradient = `linear-gradient(90deg, ${status.ok}, ${status.warn} ${(warnAt / badAt) * 100}%, ${status.bad})`;
  const metricText = (tile: HealthTile) => {
    const value = healthMetric(tile, colourBy);
    if (value === null) return colourBy === 'chg' ? 'no data yesterday' : 'n/a';
    if (colourBy === 'err') return `${value.toFixed(1)}% errors`;
    if (colourBy === 'warn') return `${value.toFixed(1)}% warn+error`;
    return `${value >= 0 ? '+' : '−'}${Math.abs(Math.round(value))}% vs yesterday`;
  };
  const errorShare =
    selectedTile && selectedTile.total > 0
      ? (selectedTile.errors / selectedTile.total) * 100
      : null;
  const change =
    selectedTile && selectedTile.previous > 0
      ? (selectedTile.total / selectedTile.previous - 1) * 100
      : null;

  return (
    <div className="flex flex-col gap-4">
      <Card
        title="Health map"
        subtitle={
          <>
            Log volume and error share from the per-minute counts · {rangeText}
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
        <div className="flex flex-wrap items-center gap-2.5">
          <span className="text-xs text-gray-500 dark:text-gray-400">Group by</span>
          <SegmentedControl
            label="Group by"
            value={groupBy}
            onChange={(value) => {
              setGroupBy(value);
              setSelected(null);
            }}
            options={[
              { value: 'k8s', label: 'Namespace → workload' },
              { value: 'host', label: 'Host → service' },
              { value: 'service', label: 'Service' },
            ]}
          />
          <span className="ml-2 text-xs text-gray-500 dark:text-gray-400">Colour by</span>
          <SegmentedControl
            label="Colour by"
            value={colourBy}
            onChange={setColourBy}
            options={[
              { value: 'err', label: 'Error share' },
              { value: 'warn', label: 'Warn + error' },
              { value: 'chg', label: 'Change vs yesterday' },
            ]}
          />
          <span className="flex-1" />
          <span className="flex items-center gap-1.5 text-xs text-gray-500 dark:text-gray-400">
            0%
            <span
              aria-hidden="true"
              className="h-2.5 w-28 rounded-full"
              style={{ background: scaleGradient }}
            />
            {colourBy === 'chg' ? `±${badAt}%+` : `${badAt}%+`}
          </span>
        </div>
        {groupBy === 'host' && (
          <span className="mt-2 block text-xs text-gray-500 dark:text-gray-400">
            Hosts come from each log&apos;s host_name field, read from the raw logs: a long range
            can be refused as too large.
          </span>
        )}
      </Card>

      {error && <Alert type="error" showIcon message={error} />}

      <div className="flex flex-wrap items-start gap-4">
        <Card
          className="min-w-0 flex-[999_1_640px]"
          title={MAP_TITLES[groupBy]}
          subtitle={`Tile size is log volume; click a tile for details${
            notShown > 0 ? ` · ${notShown} smaller tile${notShown === 1 ? '' : 's'} not shown` : ''
          }`}
        >
          {isLoading && !response ? (
            <div className="flex h-[380px] items-center justify-center">
              <Spin indicator={<LoadingOutlined spin />} />
            </div>
          ) : groups.length === 0 ? (
            <div className="flex h-[200px] items-center justify-center text-sm text-gray-500 dark:text-gray-400">
              No logs in this range
            </div>
          ) : (
            <div
              role="img"
              aria-label={`Health map of ${MAP_TITLES[groupBy].toLowerCase()}, tiles sized by log volume and coloured by ${colourBy === 'chg' ? 'change against yesterday' : 'error share'}`}
              className="flex h-[380px] gap-1"
            >
              {groups.map((g) => {
                const tiles = g.tiles.slice(0, MAX_TILES_PER_GROUP);
                return (
                  <div
                    key={g.name}
                    className="flex min-w-0 flex-col gap-1"
                    style={{ flex: `${Math.max(g.total, 1)} 1 0` }}
                  >
                    <span
                      className="truncate font-mono text-xs text-gray-500 dark:text-gray-400"
                      title={`${g.name} · ${g.total.toLocaleString()} lines`}
                    >
                      {g.name} · {formatCount(g.total)}
                    </span>
                    <div className="flex min-h-0 flex-1 flex-col gap-1">
                      {tiles.map((t) => {
                        const isSelected = selected?.group === g.name && selected?.tile === t.name;
                        const background = healthColor(healthMetric(t, colourBy), colourBy, status);
                        return (
                          <button
                            key={t.name}
                            type="button"
                            aria-pressed={isSelected}
                            title={`${t.name}: ${t.total.toLocaleString()} lines, ${metricText(t)}`}
                            onClick={() => setSelected({ group: g.name, tile: t.name })}
                            className="flex min-h-[22px] cursor-pointer flex-col justify-between overflow-hidden rounded-lg px-2 py-1.5 text-left text-white"
                            style={{
                              flex: `${Math.max(t.total, 1)} 1 0`,
                              background,
                              outline: isSelected ? `3px solid ${definition.colors.text}` : 'none',
                              outlineOffset: 1,
                              textShadow: '0 1px 2px rgba(0,0,0,.35)',
                            }}
                          >
                            <span className="truncate font-mono text-xs font-semibold">
                              {t.name}
                            </span>
                            <span className="truncate text-[11px] opacity-95">
                              {formatCount(t.total)} · {metricText(t)}
                            </span>
                          </button>
                        );
                      })}
                    </div>
                  </div>
                );
              })}
            </div>
          )}
        </Card>

        <Card
          className="min-w-0 flex-[1_1_320px]"
          title={<span className="font-mono">{selectedTile?.name ?? 'Pick a tile'}</span>}
          subtitle={
            selected
              ? `${groupBy === 'host' ? 'host ' : groupBy === 'k8s' ? 'namespace ' : ''}${selected.group}`
              : undefined
          }
          extra={
            errorShare !== null && (
              <TonePill
                text={`${errorShare.toFixed(1)}% errors`}
                tone={errorShare >= 4 ? 'bad' : errorShare >= 1 ? 'warn' : 'neutral'}
              />
            )
          }
        >
          {selectedTile && tileFilter && (
            <div className="flex flex-col gap-3">
              <div className="grid grid-cols-2 gap-2 text-sm">
                <span className="flex flex-col">
                  <span className="text-xs text-gray-500 dark:text-gray-400">Lines</span>
                  <b className="font-semibold">{selectedTile.total.toLocaleString()}</b>
                </span>
                <span className="flex flex-col">
                  <span className="text-xs text-gray-500 dark:text-gray-400">
                    Change vs yesterday
                  </span>
                  <b className="font-semibold">
                    {change === null
                      ? 'n/a'
                      : `${change >= 0 ? '+' : '−'}${Math.abs(Math.round(change))}%`}
                  </b>
                </span>
              </div>
              <strong className="text-[13px] font-semibold">Top error patterns</strong>
              {errorPatterns === null ? (
                <Spin size="small" indicator={<LoadingOutlined spin />} />
              ) : errorPatterns.length === 0 ? (
                <span className="text-sm text-gray-500 dark:text-gray-400">No error lines</span>
              ) : (
                errorPatterns.map((p) => (
                  <span
                    key={p.hash}
                    className="flex justify-between gap-2 rounded-lg bg-gray-50 px-2 py-1.5 font-mono text-xs dark:bg-gray-900/60"
                  >
                    <span className="truncate" title={p.pattern || p.sample}>
                      {p.pattern || p.sample}
                    </span>
                    <span className="text-gray-500 dark:text-gray-400">
                      {p.count.toLocaleString()}
                    </span>
                  </span>
                ))
              )}
              <span className="flex flex-wrap gap-1.5">
                <Button
                  size="small"
                  type="primary"
                  onClick={() => onOpenLogs(tileFilter.clauses, range)}
                >
                  Open logs
                </Button>
                {onOpenInfrastructure && (
                  <Button size="small" onClick={onOpenInfrastructure}>
                    Open in Infrastructure
                  </Button>
                )}
                <Button size="small" onClick={() => setIsExplaining(true)}>
                  Explain errors
                </Button>
              </span>
            </div>
          )}
        </Card>
      </div>

      {isExplaining && tileFilter && (
        <LogExplainPanelComponent
          projectId={projectId}
          query={explainQuery}
          range={explainRange}
          onFilter={(field, value, exclude) => onFilter(field, value, exclude, range)}
          onClose={() => setIsExplaining(false)}
        />
      )}
    </div>
  );
};
