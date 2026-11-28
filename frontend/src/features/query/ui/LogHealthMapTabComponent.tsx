import { LoadingOutlined } from '@ant-design/icons';
import { Alert, Button, Spin } from 'antd';
import React, { useEffect, useMemo, useRef, useState } from 'react';

import {
  type HealthGroup,
  type HealthMapGroupBy,
  type HealthMapResponse,
  type HealthTile,
  type LogPattern,
  type QueryNode,
  queryApi,
} from '../../../entity/query';
import { useTheme } from '../../../shared/theme';
import { Card } from '../../../shared/ui/v2';
import { type MapTile, layoutHealthMap } from '../lib/healthTreemap';
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
import { HealthHoneycomb } from './HealthHoneycomb';
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
// The map fills the window below its top; never shorter than this.
const MIN_MAP_HEIGHT = 380;
// The card's padding and the page's margin under the map.
const BOTTOM_SPACE = 40;
const ZOOM_BAR_HEIGHT = 32;
const VIEW_STORAGE_KEY = 'obzly-health-map-view';
type MapView = 'honeycomb' | 'treemap';

const readView = (): MapView => {
  try {
    return localStorage.getItem(VIEW_STORAGE_KEY) === 'treemap' ? 'treemap' : 'honeycomb';
  } catch {
    return 'honeycomb';
  }
};
const GROUP_HEADER = 20;
// Below these a tile or group can't show its name, so it rolls into "+N more".
const TILE_MIN = { w: 64, h: 34 };
const GROUP_MIN = { w: 96, h: 60 };
const TOP_ERROR_PATTERNS = 3;
const PATTERN_SAMPLE_ROWS = 20_000;
const MAP_TITLES: Record<HealthMapGroupBy, string> = {
  k8s: 'Namespaces and workloads',
  host: 'Hosts and services',
  service: 'Services',
};
const GROUP_NOUNS: Record<HealthMapGroupBy, string> = {
  k8s: 'namespaces',
  host: 'hosts',
  service: 'groups',
};

/** A zoomed-in part of the map: some groups, or one group's tiles. */
interface Zoom {
  label: string;
  groups?: string[];
  group?: string;
  tiles?: string[];
}

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
  const [zoom, setZoom] = useState<Zoom[]>([]);
  const [mapView, setMapView] = useState<MapView>(readView);
  const mapRef = useRef<HTMLDivElement>(null);
  const [mapWidth, setMapWidth] = useState(0);
  const [mapHeight, setMapHeight] = useState(MIN_MAP_HEIGHT);

  useEffect(() => {
    const element = mapRef.current;
    if (!element) return;
    const observer = new ResizeObserver(([entry]) =>
      setMapWidth(Math.floor(entry.contentRect.width)),
    );
    observer.observe(element);
    return () => observer.disconnect();
  }, []);

  // Measured on load and resize, not on scroll, so the map doesn't change size under the reader.
  const hasResponse = response !== null;
  useEffect(() => {
    const fit = () => {
      const element = mapRef.current;
      if (!element) return;
      const room = window.innerHeight - element.getBoundingClientRect().top - BOTTOM_SPACE;
      setMapHeight(
        Math.max(MIN_MAP_HEIGHT, Math.floor(Math.min(room, window.innerHeight - BOTTOM_SPACE))),
      );
    };
    fit();
    window.addEventListener('resize', fit);
    return () => window.removeEventListener('resize', fit);
  }, [hasResponse, groupBy, error]);

  useEffect(() => {
    const abort = new AbortController();
    setIsLoading(true);
    setError(null);
    setZoom([]);
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

  const groups = response?.groups ?? [];
  const notShown = response?.hiddenTiles ?? 0;
  const zoomed = zoom[zoom.length - 1] as Zoom | undefined;
  const treemapHeight = mapHeight - (zoom.length > 0 ? ZOOM_BAR_HEIGHT : 0);
  const blocks = useMemo(() => {
    let view: HealthGroup[] = response?.groups ?? [];
    if (zoomed?.groups) view = view.filter((g) => zoomed.groups?.includes(g.name));
    if (zoomed?.group) {
      const group = view.find((g) => g.name === zoomed.group);
      const tiles = (group?.tiles ?? []).filter(
        (t) => !zoomed.tiles || zoomed.tiles.includes(t.name),
      );
      view = group ? [{ ...group, tiles, total: tiles.reduce((sum, t) => sum + t.total, 0) }] : [];
    }
    if (!mapWidth) return [];
    return layoutHealthMap(view, mapWidth, treemapHeight, {
      tile: TILE_MIN,
      group: GROUP_MIN,
      header: zoomed?.group ? 0 : GROUP_HEADER,
    });
  }, [response, zoomed, mapWidth, treemapHeight]);
  const zoomInto = (next: Zoom) => setZoom((stack) => [...stack, next]);
  const openTile = (group: string, tile: MapTile) =>
    tile.merged
      ? zoomInto({
          label: `${tile.merged.length} smaller in ${group}`,
          group,
          tiles: tile.merged,
        })
      : setSelected({ group, tile: tile.tile.name });
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
          <span className="ml-2 text-xs text-gray-500 dark:text-gray-400">View</span>
          <SegmentedControl
            label="View"
            value={mapView}
            onChange={(value) => {
              setMapView(value);
              try {
                localStorage.setItem(VIEW_STORAGE_KEY, value);
              } catch {
                /* the next visit opens the honeycomb */
              }
            }}
            options={[
              { value: 'honeycomb', label: 'Honeycomb' },
              { value: 'treemap', label: 'Treemap' },
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
          subtitle={`${
            mapView === 'honeycomb'
              ? 'One hexagon per workload, sized by log volume within its namespace, worst first'
              : 'Tile size is log volume'
          }; hover for its name, click for details${
            notShown > 0 ? ` · ${notShown} smaller tile${notShown === 1 ? '' : 's'} not shown` : ''
          }`}
        >
          <div ref={mapRef}>
            {isLoading && !response ? (
              <div className="flex items-center justify-center" style={{ height: mapHeight }}>
                <Spin indicator={<LoadingOutlined spin />} />
              </div>
            ) : groups.length === 0 ? (
              <div className="flex h-[200px] items-center justify-center text-sm text-gray-500 dark:text-gray-400">
                No logs in this range
              </div>
            ) : null}
            {mapView === 'honeycomb' && response && groups.length > 0 && mapWidth > 0 && (
              <HealthHoneycomb
                groups={groups}
                metric={(tile) => healthMetric(tile, colourBy)}
                color={(tile) => healthColor(healthMetric(tile, colourBy), colourBy, status)}
                metricText={metricText}
                selected={selected}
                selectedColor={definition.colors.text}
                onSelect={(group, tile) => setSelected({ group, tile })}
                width={mapWidth}
                height={mapHeight}
              />
            )}
            {mapView === 'treemap' && zoom.length > 0 && (
              <nav
                aria-label="Health map zoom"
                className="mb-2 flex flex-wrap items-center gap-1 text-xs text-gray-500 dark:text-gray-400"
              >
                <Button size="small" type="link" className="!px-0" onClick={() => setZoom([])}>
                  All {GROUP_NOUNS[groupBy]}
                </Button>
                {zoom.map((z, i) => (
                  <React.Fragment key={i}>
                    <span aria-hidden="true">›</span>
                    {i === zoom.length - 1 ? (
                      <span className="font-mono text-gray-700 dark:text-gray-200">{z.label}</span>
                    ) : (
                      <Button
                        size="small"
                        type="link"
                        className="!px-0 font-mono"
                        onClick={() => setZoom(zoom.slice(0, i + 1))}
                      >
                        {z.label}
                      </Button>
                    )}
                  </React.Fragment>
                ))}
              </nav>
            )}
            <div
              role="group"
              aria-label={`Health map of ${MAP_TITLES[groupBy].toLowerCase()}, tiles sized by log volume and coloured by ${colourBy === 'chg' ? 'change against yesterday' : 'error share'}`}
              className="relative"
              style={{ height: treemapHeight }}
              hidden={!response || groups.length === 0 || mapView !== 'treemap'}
            >
              {blocks.map((b) =>
                b.merged ? (
                  <HealthTileButton
                    key={`merged:${b.group.name}`}
                    mapTile={b.tiles[0]}
                    label={`+${b.merged.length} more ${GROUP_NOUNS[groupBy]}`}
                    metricText={metricText}
                    background={healthColor(
                      healthMetric(b.tiles[0].tile, colourBy),
                      colourBy,
                      status,
                    )}
                    isSelected={false}
                    selectedColor={definition.colors.text}
                    onClick={() =>
                      zoomInto({
                        label: `${b.merged?.length} smaller ${GROUP_NOUNS[groupBy]}`,
                        groups: b.merged,
                      })
                    }
                  />
                ) : (
                  <React.Fragment key={b.group.name}>
                    {!zoomed?.group && (
                      <button
                        type="button"
                        title={`${b.group.name} · ${b.group.total.toLocaleString()} lines · click to zoom in`}
                        onClick={() => zoomInto({ label: b.group.name, group: b.group.name })}
                        className="absolute cursor-pointer truncate border-0 bg-transparent p-0 text-left font-mono text-xs text-gray-500 hover:text-gray-800 dark:text-gray-400 dark:hover:text-gray-100"
                        style={{
                          left: b.rect.x + 2,
                          top: b.rect.y,
                          width: Math.max(b.rect.w - 4, 0),
                          height: GROUP_HEADER,
                        }}
                      >
                        {b.group.name} · {formatCount(b.group.total)}
                      </button>
                    )}
                    {b.tiles.map((t) => (
                      <HealthTileButton
                        key={t.tile.name}
                        mapTile={t}
                        label={t.tile.name}
                        metricText={metricText}
                        background={healthColor(healthMetric(t.tile, colourBy), colourBy, status)}
                        isSelected={
                          !t.merged &&
                          selected?.group === b.group.name &&
                          selected?.tile === t.tile.name
                        }
                        selectedColor={definition.colors.text}
                        onClick={() => openTile(b.group.name, t)}
                      />
                    ))}
                  </React.Fragment>
                ),
              )}
            </div>
          </div>
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

interface HealthTileButtonProps {
  mapTile: MapTile;
  label: string;
  metricText: (tile: HealthTile) => string;
  background: string;
  isSelected: boolean;
  selectedColor: string;
  onClick: () => void;
}

// Labels only where they fit: a clipped second line reads as noise.
const HealthTileButton = ({
  mapTile,
  label,
  metricText,
  background,
  isSelected,
  selectedColor,
  onClick,
}: HealthTileButtonProps): React.JSX.Element => {
  const { rect, tile, merged } = mapTile;
  const width = Math.max(rect.w - 4, 1);
  const height = Math.max(rect.h - 4, 1);
  const showName = width >= 36 && height >= 26;
  const showStats = width >= 64 && height >= 42;
  const stats = `${formatCount(tile.total)} · ${metricText(tile)}`;
  return (
    <button
      type="button"
      aria-pressed={merged ? undefined : isSelected}
      title={
        merged
          ? `${label}: ${tile.total.toLocaleString()} lines, ${metricText(tile)} · click to zoom in`
          : `${label}: ${tile.total.toLocaleString()} lines, ${metricText(tile)}`
      }
      onClick={onClick}
      className="absolute flex cursor-pointer flex-col justify-between overflow-hidden rounded-lg border-0 px-2 py-1.5 text-left text-white"
      style={{
        left: rect.x + 2,
        top: rect.y + 2,
        width,
        height,
        background,
        boxShadow: merged ? 'inset 0 0 0 1px rgba(255,255,255,.55)' : undefined,
        outline: isSelected ? `3px solid ${selectedColor}` : 'none',
        outlineOffset: 1,
        textShadow: '0 1px 2px rgba(0,0,0,.35)',
        padding: showName ? undefined : 0,
      }}
    >
      {showName && (
        <span className="truncate font-mono text-xs font-semibold">
          {label}
          {merged ? ' ›' : ''}
        </span>
      )}
      {showStats && <span className="truncate text-[11px] opacity-95">{stats}</span>}
    </button>
  );
};
