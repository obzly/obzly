import {
  CaretRightOutlined,
  CloseOutlined,
  LoadingOutlined,
  PauseOutlined,
} from '@ant-design/icons';
import { Button, Empty, Segmented, Select, Spin, Switch, Tooltip } from 'antd';
import type { EChartsOption } from 'echarts';
import type { ECharts } from 'echarts/core';
import type { GraphSeriesOption } from 'echarts/types/dist/shared';
import type { CallbackDataParams } from 'echarts/types/dist/shared';
import React, { useEffect, useMemo, useRef, useState } from 'react';

import {
  type ServiceMapEdge,
  type ServiceMapHealth,
  type ServiceMapNode,
  type ServiceMapNodeType,
  serviceMapApi,
} from '../../../entity/serviceMap';
import { escapeHtml, useVisibleInterval } from '../../../shared/lib';
import { type StatusColors, type ThemeDefinition, useTheme } from '../../../shared/theme';
import { Card, PageHeader } from '../../../shared/ui/v2';
import { type TimeRange, TimeRangePickerComponent } from '../../query';
import { colorForService } from '../../traces/ui/traceColors';
import echarts from './echartsSetup';

interface Props {
  projectId: string;
  contentHeight: number;
  /** Opens the Traces tab filtered to a service. */
  onViewTraces?: (serviceName: string) => void;
}

const OTHER_NODE_NAME = 'other';
const OTHER_NODE_COLOR = '#9ca3af'; // gray-400 - visually distinct from the service palette

const MIN_NODE_SIZE = 24;
const MAX_NODE_SIZE = 70;
const MIN_EDGE_WIDTH = 1;
const MAX_EDGE_WIDTH = 10;

// Live mode polls a short rolling window; 60s keeps the backend's per-request
// self-join cheap at a 5s poll.
const LIVE_WINDOW_SECONDS = 60;
const LIVE_POLL_INTERVAL_MS = 5000;

// 'health' colours the nodes by their health (and the edges by error rate).
type ColorMode = 'latency' | 'errors' | 'health';
const COLOR_MODE_STORAGE_KEY = 'obzly-service-map-color-by';

const readColorMode = (): ColorMode => {
  try {
    const stored = localStorage.getItem(COLOR_MODE_STORAGE_KEY);
    return stored === 'errors' || stored === 'health' ? stored : 'latency';
  } catch {
    return 'latency';
  }
};

// Edges have no health of their own; in health mode they show the error rate.
const edgeModeOf = (colorMode: ColorMode): 'latency' | 'errors' =>
  colorMode === 'latency' ? 'latency' : 'errors';

const NODE_TYPE_LABEL: Record<ServiceMapNodeType, string> = {
  service: 'Service',
  database: 'Database',
  cache: 'Cache',
  messaging: 'Messaging',
  external: 'External',
};

const NODE_TYPE_DESCRIPTION: Record<ServiceMapNodeType, string> = {
  service: 'Service · OpenTelemetry',
  database: 'Database · seen from client spans',
  cache: 'Cache · seen from client spans',
  messaging: 'Message broker · seen from client spans',
  external: 'External dependency · seen from client spans',
};

// Thresholds live in the backend (servicemap/node_stats.go).
const HEALTH_LABEL: Record<ServiceMapHealth, string> = {
  ok: 'Healthy',
  degraded: 'Degraded',
  failing: 'Failing',
};
const HEALTH_CANVAS_KEY: Record<ServiceMapHealth, 'ok' | 'warn' | 'bad'> = {
  ok: 'ok',
  degraded: 'warn',
  failing: 'bad',
};

// Latency colors, interpolated between three stops so the gradient reads
// continuously.
const LATENCY_COLOR_STOPS: [number, [number, number, number]][] = [
  [0, [34, 197, 94]], // green-500
  [200, [234, 179, 8]], // yellow-500
  [800, [239, 68, 68]], // red-500 (800ms+ is already "bad", no need to keep climbing)
];

function latencyColor(ms: number): string {
  const clamped = Math.max(0, ms);
  for (let i = 0; i < LATENCY_COLOR_STOPS.length - 1; i++) {
    const [fromMs, fromRgb] = LATENCY_COLOR_STOPS[i];
    const [toMs, toRgb] = LATENCY_COLOR_STOPS[i + 1];
    if (clamped <= toMs || i === LATENCY_COLOR_STOPS.length - 2) {
      const ratio = toMs === fromMs ? 1 : Math.min(1, (clamped - fromMs) / (toMs - fromMs));
      const [r, g, b] = fromRgb.map((c, idx) => Math.round(c + (toRgb[idx] - c) * ratio));
      return `rgb(${r}, ${g}, ${b})`;
    }
  }
  return `rgb(${LATENCY_COLOR_STOPS[0][1].join(', ')})`;
}

// Error rate bands: under 1% fine, 1-5% degraded, over 5% failing.
type ErrorBand = 'ok' | 'warn' | 'bad';
const errorBand = (errorRate: number): ErrorBand =>
  errorRate >= 0.05 ? 'bad' : errorRate >= 0.01 ? 'warn' : 'ok';
// Canvas can't read CSS variables, so the theme's colours are passed in.
// Health and error bands are status colours (green stays green in every theme).
interface CanvasColors {
  ok: string;
  warn: string;
  bad: string;
  panel: string;
  border: string;
  text: string;
  muted: string;
  edge: string;
  /** Per-service node colours (the theme's chart series). */
  series: string[];
}

// Neutral edge grey per scheme; ECharts draws graph edges at half opacity.
const EDGE_GREY = { light: '#9aaba4', dark: '#6b7d76' };

const canvasColorsOf = (
  definition: ThemeDefinition,
  status: StatusColors,
  series: string[],
): CanvasColors => ({
  ok: status.ok,
  warn: status.warn,
  bad: status.bad,
  panel: definition.colors.panel,
  border: definition.colors.border,
  text: definition.colors.text,
  muted: definition.colors.muted,
  edge: EDGE_GREY[definition.scheme],
  series,
});

const formatBytes = (bytes: number): string => {
  if (!bytes || bytes <= 0) return '0 B';
  const units = ['B', 'KB', 'MB', 'GB', 'TB'];
  const exponent = Math.min(Math.floor(Math.log(bytes) / Math.log(1024)), units.length - 1);
  const value = bytes / 1024 ** exponent;
  return `${value.toFixed(value >= 10 || exponent === 0 ? 0 : 1)} ${units[exponent]}`;
};

const formatErrorRate = (errorRate: number): string => `${(errorRate * 100).toFixed(1)}%`;
const formatMs = (ms: number): string =>
  ms >= 1000 ? `${(ms / 1000).toFixed(1)} s` : `${ms.toFixed(ms < 10 ? 1 : 0)} ms`;
const formatPerMinute = (perSecond: number): string => {
  const perMinute = perSecond * 60;
  return `${perMinute.toLocaleString(undefined, { maximumFractionDigits: perMinute < 10 ? 1 : 0 })}/min`;
};
const formatRps = (rps: number): string => `${rps < 1 ? rps.toFixed(2) : rps.toFixed(1)} req/s`;
const formatBps = (bytesPerSecond: number): string => `${formatBytes(bytesPerSecond)}/s`;

// "15 minutes", "6 hours", "3 days" for the subtitle.
const describeSpan = (ms: number): string => {
  const minutes = ms / 60_000;
  const fmt = (value: number, unit: string) => {
    const rounded = Number(value.toFixed(1));
    return `${rounded} ${unit}${rounded === 1 ? '' : 's'}`;
  };
  if (minutes < 60) return fmt(Math.round(minutes), 'minute');
  if (minutes < 60 * 48) return fmt(minutes / 60, 'hour');
  return fmt(minutes / 1440, 'day');
};

// Traffic weight for sizing, as rates so live and historical windows look
// comparable. Bytes/sec is scaled to KB/s to sit near call rates (a heuristic).
const edgeWeight = (edge: ServiceMapEdge, windowSeconds: number): number =>
  (edge.callCount ?? 0) / windowSeconds + (edge.bytesTotal ?? 0) / windowSeconds / 1024;

const clamp = (value: number, min: number, max: number): number =>
  Math.max(min, Math.min(max, value));

const edgeKey = (edge: ServiceMapEdge): string => `${edge.src}→${edge.dst}`;

interface GraphNode {
  id: string;
  name: string;
  symbolSize: number;
  itemStyle: { color: string; borderColor?: string; borderWidth?: number; borderType?: string };
  sources: ServiceMapNode['sources'];
  traffic: number;
  namespace?: string;
  deployment?: string;
  node?: string;
  nodeType?: ServiceMapNodeType;
  requests?: number;
  errors?: number;
  errorRate?: number;
  p95LatencyMs?: number;
  health?: ServiceMapHealth;
}

const UNKNOWN_NAMESPACE = 'unknown';

// Nodes keep per-service colors (shared with the trace waterfall); ECharts
// categories would force one color per namespace.
function namespaceOf(node: ServiceMapNode): string {
  return node.namespace || UNKNOWN_NAMESPACE;
}

interface GraphEdge {
  source: string;
  target: string;
  lineStyle: { width: number; type: 'solid' | 'dashed'; color?: string };
  label?: { show: boolean; formatter: string };
  edge: ServiceMapEdge;
}

interface GraphSeriesData {
  each(callback: (index: number) => void): void;
  getId(index: number): string;
  getItemLayout(index: number): number[] | undefined;
}

// The settled/dragged position of every node lives in the series' item
// layout, not in the option data (which never contains simulated x/y).
function readGraphSeriesData(chart: ECharts): GraphSeriesData | null {
  const model = (
    chart as unknown as {
      getModel(): { getSeriesByIndex(index: number): { getData(): GraphSeriesData } | undefined };
    }
  ).getModel();
  return model.getSeriesByIndex(0)?.getData() ?? null;
}

// The edge colour for the chosen mode; undefined (the default grey) when the
// edge has no such number, e.g. a network-only edge.
function edgeColor(
  edge: ServiceMapEdge,
  colorMode: ColorMode,
  colors: CanvasColors,
): string | undefined {
  if (!edge.sources.includes('spans')) return undefined;
  if (edgeModeOf(colorMode) === 'errors') {
    return edge.errorRate !== undefined ? colors[errorBand(edge.errorRate)] : undefined;
  }
  return edge.avgLatencyMs !== undefined ? latencyColor(edge.avgLatencyMs) : undefined;
}

// Labels only edges that need a look, so a healthy map stays quiet.
function edgeLabel(edge: ServiceMapEdge, colorMode: ColorMode): string | null {
  if (!edge.sources.includes('spans')) return null;
  if (edgeModeOf(colorMode) === 'errors') {
    return edge.errorRate !== undefined && errorBand(edge.errorRate) !== 'ok'
      ? `${formatErrorRate(edge.errorRate)} errors`
      : null;
  }
  return edge.avgLatencyMs !== undefined && edge.avgLatencyMs >= 200
    ? `${edge.avgLatencyMs.toFixed(0)} ms avg`
    : null;
}

function buildGraphData(
  nodes: ServiceMapNode[],
  edges: ServiceMapEdge[],
  windowSeconds: number,
  colorMode: ColorMode,
  colors: CanvasColors,
) {
  const trafficByNode = new Map<string, number>();
  for (const edge of edges) {
    const weight = edgeWeight(edge, windowSeconds);
    trafficByNode.set(edge.src, (trafficByNode.get(edge.src) ?? 0) + weight);
    trafficByNode.set(edge.dst, (trafficByNode.get(edge.dst) ?? 0) + weight);
  }
  const maxTraffic = Math.max(1, ...Array.from(trafficByNode.values()));

  const graphNodes: GraphNode[] = nodes.map((node) => {
    const traffic = trafficByNode.get(node.name) ?? 0;
    const isOther = node.name === OTHER_NODE_NAME;
    const sizeRatio = Math.sqrt(traffic / maxTraffic);
    const symbolSize = clamp(
      MIN_NODE_SIZE + sizeRatio * (MAX_NODE_SIZE - MIN_NODE_SIZE),
      MIN_NODE_SIZE,
      MAX_NODE_SIZE,
    );

    // Health mode: unknown health (network-only, no requests) stays grey.
    const color =
      colorMode === 'health'
        ? node.health
          ? colors[HEALTH_CANVAS_KEY[node.health]]
          : OTHER_NODE_COLOR
        : colorForService(node.name, colors.series);

    return {
      id: node.name,
      name: node.name,
      symbolSize,
      itemStyle: isOther
        ? { color: OTHER_NODE_COLOR, borderColor: '#4b5563', borderWidth: 2, borderType: 'dashed' }
        : {
            color,
            // External dependencies draw dashed, as on the mock.
            ...(node.type === 'external'
              ? { borderColor: colors.edge, borderWidth: 2, borderType: 'dashed' }
              : {}),
          },
      sources: node.sources,
      traffic,
      namespace: node.namespace,
      deployment: node.deployment,
      node: node.node,
      nodeType: node.type,
      requests: node.requests,
      errors: node.errors,
      errorRate: node.errorRate,
      p95LatencyMs: node.p95LatencyMs,
      health: node.health,
    };
  });

  const maxEdgeWeight = Math.max(1, ...edges.map((edge) => edgeWeight(edge, windowSeconds)));

  const graphEdges: GraphEdge[] = edges.map((edge) => {
    const hasSpans = edge.sources.includes('spans');
    const hasNetwork = edge.sources.includes('network');
    const weightRatio = edgeWeight(edge, windowSeconds) / maxEdgeWeight;
    const width = clamp(
      MIN_EDGE_WIDTH + Math.sqrt(weightRatio) * (MAX_EDGE_WIDTH - MIN_EDGE_WIDTH),
      MIN_EDGE_WIDTH,
      MAX_EDGE_WIDTH,
    );
    const label = edgeLabel(edge, colorMode);

    return {
      source: edge.src,
      target: edge.dst,
      lineStyle: {
        // Edges seen in spans draw solid; network-only edges are a weaker inference.
        type: hasSpans ? 'solid' : 'dashed',
        width: hasSpans && hasNetwork ? width + 1 : width,
        // Spans edges are colored by latency or error rate; network-only edges have neither.
        color: edgeColor(edge, colorMode, colors),
      },
      label: label ? { show: true, formatter: label } : undefined,
      edge,
    };
  });

  return { graphNodes, graphEdges };
}

const Swatch = ({ color, label }: { color: string; label: string }) => (
  <span className="inline-flex items-center gap-1.5">
    <i className="inline-block h-2.5 w-2.5 rounded-sm" style={{ background: color }} />
    {label}
  </span>
);

const LineSample = ({ dashed, label }: { dashed: boolean; label: string }) => (
  <span className="inline-flex items-center gap-1.5">
    <i
      className={`inline-block w-3.5 border-t-2 border-gray-400 dark:border-gray-500 ${
        dashed ? 'border-dashed' : 'border-solid'
      }`}
    />
    {label}
  </span>
);

const Field = ({ label, children }: { label: string; children: React.ReactNode }) => (
  <>
    <dt className="font-medium text-gray-500 dark:text-gray-400">{label}</dt>
    <dd className="m-0 min-w-0 break-words text-gray-800 dark:text-gray-100">{children}</dd>
  </>
);

export const ServiceMapComponent = ({
  projectId,
  contentHeight,
  onViewTraces,
}: Props): React.JSX.Element => {
  const { definition, chartColors, status } = useTheme();
  const containerRef = useRef<HTMLDivElement>(null);
  const chartRef = useRef<ECharts | null>(null);

  const [timeRange, setTimeRange] = useState<TimeRange | null>(null);
  const [nodes, setNodes] = useState<ServiceMapNode[]>([]);
  const [edges, setEdges] = useState<ServiceMapEdge[]>([]);
  const [isLoading, setIsLoading] = useState(false);
  const [hasLoaded, setHasLoaded] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [windowSeconds, setWindowSeconds] = useState(1);

  const [isLive, setIsLive] = useState(false);
  const [isPaused, setIsPaused] = useState(false);
  const [lastUpdated, setLastUpdated] = useState<Date | null>(null);
  // Guards against a slow poll overlapping the next tick's fetch.
  const pollInFlightRef = useRef(false);

  // By id, so a live refresh updates an open panel instead of freezing it.
  const [selectedNodeId, setSelectedNodeId] = useState<string | null>(null);
  const [selectedEdgeKey, setSelectedEdgeKey] = useState<string | null>(null);
  // The node whose panel an edge was opened from, for the "back" button.
  const [edgeBackNodeId, setEdgeBackNodeId] = useState<string | null>(null);

  const [colorMode, setColorMode] = useState<ColorMode>(readColorMode);
  useEffect(() => {
    try {
      localStorage.setItem(COLOR_MODE_STORAGE_KEY, colorMode);
    } catch {
      // not kept, still works
    }
  }, [colorMode]);

  const fetchServiceMap = (
    from: { toISOString(): string; valueOf(): number },
    to: { toISOString(): string; valueOf(): number },
  ) => {
    if (pollInFlightRef.current) return;
    pollInFlightRef.current = true;
    setError(null);

    return serviceMapApi
      .getServiceMap(projectId, from.toISOString(), to.toISOString())
      .then((response) => {
        setNodes(response.nodes);
        setEdges(response.edges);
        // The backend's window, since it clamps long ranges.
        setWindowSeconds(
          Math.max(1, response.windowSeconds ?? (to.valueOf() - from.valueOf()) / 1000),
        );
        setLastUpdated(new Date());
      })
      .catch((e: unknown) => {
        setError((e as Error).message);
      })
      .finally(() => {
        pollInFlightRef.current = false;
        setIsLoading(false);
        setHasLoaded(true);
      });
  };

  // Historical mode: one fetch per picker selection.
  useEffect(() => {
    if (isLive || !timeRange) return;
    setIsLoading(true);
    void fetchServiceMap(timeRange.from, timeRange.to);
  }, [projectId, timeRange, isLive]);

  // Live mode: poll a rolling window; pausing keeps the last graph.
  const pollLive = () => {
    const to = new Date();
    const from = new Date(to.getTime() - LIVE_WINDOW_SECONDS * 1000);
    void fetchServiceMap(from, to);
  };
  useEffect(() => {
    if (!isLive || isPaused) return;

    setIsLoading(true);
    pollLive();
  }, [projectId, isLive, isPaused]);
  // Paused in hidden tabs, so a forgotten live map stops polling.
  useVisibleInterval(pollLive, LIVE_POLL_INTERVAL_MS, isLive && !isPaused);

  // Empty = all namespaces. Multiple selections show services in any of them,
  // so a service's dependencies across namespaces can be viewed together.
  const [namespaceFilter, setNamespaceFilter] = useState<string[]>([]);

  const availableNamespaces = useMemo(() => {
    const set = new Set<string>();
    for (const node of nodes) {
      if (node.name === OTHER_NODE_NAME) continue;
      set.add(namespaceOf(node));
    }
    return Array.from(set).sort();
  }, [nodes]);

  const { filteredNodes, filteredEdges } = useMemo(() => {
    if (namespaceFilter.length === 0) return { filteredNodes: nodes, filteredEdges: edges };
    const selectedNamespaces = new Set(namespaceFilter);
    const keptNames = new Set(
      nodes
        .filter((n) => n.name === OTHER_NODE_NAME || selectedNamespaces.has(namespaceOf(n)))
        .map((n) => n.name),
    );
    return {
      filteredNodes: nodes.filter((n) => keptNames.has(n.name)),
      filteredEdges: edges.filter((e) => keptNames.has(e.src) && keptNames.has(e.dst)),
    };
  }, [nodes, edges, namespaceFilter]);

  const canvasColors = useMemo(
    () => canvasColorsOf(definition, status, chartColors),
    [definition, status, chartColors],
  );

  const { graphNodes, graphEdges } = useMemo(
    () => buildGraphData(filteredNodes, filteredEdges, windowSeconds, colorMode, canvasColors),
    [filteredNodes, filteredEdges, windowSeconds, colorMode, canvasColors],
  );

  // Keeps every node where the user put it. Force layout only places nodes
  // with no saved position, then the chart switches to a fixed layout: in force
  // layout ECharts releases a dragged node and the simulation pulls it back.
  const nodePositionsRef = useRef<Map<string, { x: number; y: number }>>(new Map());
  const [layoutVersion, setLayoutVersion] = useState(0);

  useEffect(() => {
    if (!containerRef.current) return;
    if (graphNodes.length === 0) {
      chartRef.current?.dispose();
      chartRef.current = null;
      return;
    }

    const chart = chartRef.current ?? echarts.init(containerRef.current);
    chartRef.current = chart;

    const positions = nodePositionsRef.current;
    const isSettled = graphNodes.every((node) => positions.has(node.id));
    const positionedGraphNodes = graphNodes.map((node) => {
      const saved = positions.get(node.id);
      return saved ? { ...node, x: saved.x, y: saved.y, fixed: true } : node;
    });

    const option: EChartsOption = {
      tooltip: {
        backgroundColor: canvasColors.panel,
        borderColor: canvasColors.border,
        textStyle: { color: canvasColors.text },
        formatter: (rawParams: CallbackDataParams | CallbackDataParams[]) => {
          const params = Array.isArray(rawParams) ? rawParams[0] : rawParams;
          if (!params) return '';
          if (params.dataType === 'edge') {
            const edge = (params.data as GraphEdge).edge;
            const lines = [
              `${escapeHtml(edge.src)} → ${escapeHtml(edge.dst)}`,
              `Sources: ${escapeHtml(edge.sources.join(', '))}`,
            ];
            if (edge.callCount !== undefined) {
              lines.push(
                `Calls: ${edge.callCount.toLocaleString()} (${formatRps(edge.callCount / windowSeconds)})`,
              );
            }
            if (edge.avgLatencyMs !== undefined)
              lines.push(`Avg latency: ${edge.avgLatencyMs.toFixed(1)} ms`);
            if (edge.errorRate !== undefined)
              lines.push(`Error rate: ${formatErrorRate(edge.errorRate)}`);
            if (edge.bytesTotal !== undefined) {
              lines.push(
                `Bytes: ${formatBytes(edge.bytesTotal)} (${formatBps(edge.bytesTotal / windowSeconds)})`,
              );
            }
            return lines.join('<br/>');
          }
          const node = params.data as GraphNode;
          const lines = [
            escapeHtml(node.name),
            `Sources: ${escapeHtml(node.sources.join(', ')) || 'none'}`,
          ];
          if (node.namespace) lines.push(`Namespace: ${escapeHtml(node.namespace)}`);
          if (node.deployment) lines.push(`Deployment: ${escapeHtml(node.deployment)}`);
          if (node.node) lines.push(`Node: ${escapeHtml(node.node)}`);
          if (node.nodeType) lines.push(`Type: ${NODE_TYPE_LABEL[node.nodeType]}`);
          if (node.requests !== undefined) {
            lines.push(`Requests: ${formatPerMinute(node.requests / windowSeconds)}`);
          }
          if (node.errorRate !== undefined)
            lines.push(`Errors: ${formatErrorRate(node.errorRate)}`);
          if (node.p95LatencyMs !== undefined) lines.push(`p95: ${formatMs(node.p95LatencyMs)}`);
          if (node.health) lines.push(`Health: ${HEALTH_LABEL[node.health]}`);
          return lines.join('<br/>');
        },
      },
      series: [
        {
          type: 'graph',
          layout: isSettled ? 'none' : 'force',
          roam: true,
          draggable: true,
          label: {
            show: true,
            position: 'right',
            color: canvasColors.text,
            // The type on a second, muted line.
            formatter: (params: CallbackDataParams) => {
              const node = params.data as GraphNode;
              const name = node.name.replace(/[{}|]/g, '');
              return node.nodeType
                ? `{name|${name}}\n{type|${NODE_TYPE_LABEL[node.nodeType]}}`
                : `{name|${name}}`;
            },
            rich: {
              name: { color: canvasColors.text, fontSize: 12 },
              type: { color: canvasColors.muted, fontSize: 10, padding: [2, 0, 0, 0] },
            },
          },
          edgeLabel: {
            fontSize: 11,
            color: canvasColors.text,
            backgroundColor: canvasColors.panel,
            borderColor: canvasColors.border,
            borderWidth: 1,
            borderRadius: 4,
            padding: [2, 5],
          },
          force: {
            repulsion: 180,
            edgeLength: [60, 160],
            gravity: 0.1,
          },
          edgeSymbol: ['none', 'arrow'],
          edgeSymbolSize: 8,
          data: positionedGraphNodes,
          links: graphEdges,
          lineStyle: {
            color: canvasColors.edge,
            curveness: 0.15,
          },
          emphasis: {
            focus: 'adjacency',
            lineStyle: { width: 4 },
          },
        } as GraphSeriesOption,
      ],
    };

    chart.setOption(option, true);

    // Returns true if it recorded a position for a node that had none.
    const capturePositions = (): boolean => {
      const data = readGraphSeriesData(chart);
      if (!data) return false;
      let addedNew = false;
      data.each((index) => {
        const point = data.getItemLayout(index);
        if (!point || !Number.isFinite(point[0]) || !Number.isFinite(point[1])) return;
        const id = data.getId(index);
        if (!positions.has(id)) addedNew = true;
        positions.set(id, { x: point[0], y: point[1] });
      });
      return addedNew;
    };

    chart.off('finished');
    chart.on('finished', () => {
      // First layout of new nodes just settled: re-render with everything
      // fixed so drags stick from here on.
      if (capturePositions() && !isSettled) setLayoutVersion((v) => v + 1);
    });

    const zr = chart.getZr();
    zr.on('mouseup', capturePositions);

    chart.off('click');
    chart.on('click', (params) => {
      if (params.dataType === 'node') {
        setSelectedNodeId((params.data as GraphNode).id);
        setSelectedEdgeKey(null);
        setEdgeBackNodeId(null);
      } else if (params.dataType === 'edge') {
        setSelectedEdgeKey(edgeKey((params.data as GraphEdge).edge));
        setEdgeBackNodeId(null);
      }
    });

    return () => {
      zr.off('mouseup', capturePositions);
    };
  }, [graphNodes, graphEdges, canvasColors, windowSeconds, layoutVersion]);

  // Follows the card's width too (the details panel opening beside it), not
  // just the window.
  const hasGraph = !error && graphNodes.length > 0;
  useEffect(() => {
    const container = containerRef.current;
    if (!container) return;
    const handleResize = () => chartRef.current?.resize();
    window.addEventListener('resize', handleResize);
    const observer =
      typeof ResizeObserver === 'undefined' ? null : new ResizeObserver(handleResize);
    observer?.observe(container);
    return () => {
      window.removeEventListener('resize', handleResize);
      observer?.disconnect();
    };
  }, [hasGraph]);

  const resetLayout = () => {
    nodePositionsRef.current.clear();
    setLayoutVersion((v) => v + 1);
  };

  useEffect(() => {
    return () => {
      chartRef.current?.dispose();
      chartRef.current = null;
    };
  }, []);

  const closePanel = () => {
    setSelectedNodeId(null);
    setSelectedEdgeKey(null);
    setEdgeBackNodeId(null);
  };

  const selectedNode = graphNodes.find((n) => n.id === selectedNodeId) ?? null;
  const selectedEdge = graphEdges.find((e) => edgeKey(e.edge) === selectedEdgeKey) ?? null;
  const backNode = graphNodes.find((n) => n.id === edgeBackNodeId) ?? null;

  const linksOf = (nodeId: string) => ({
    inbound: filteredEdges.filter((e) => e.dst === nodeId),
    outbound: filteredEdges.filter((e) => e.src === nodeId),
  });

  const edgeMetric = (edge: ServiceMapEdge): { text: string; color?: string } | null => {
    if (edgeModeOf(colorMode) === 'errors') {
      return edge.errorRate === undefined
        ? null
        : {
            text: formatErrorRate(edge.errorRate),
            color: canvasColors[errorBand(edge.errorRate)],
          };
    }
    return edge.avgLatencyMs === undefined
      ? null
      : { text: `${edge.avgLatencyMs.toFixed(1)} ms`, color: latencyColor(edge.avgLatencyMs) };
  };

  const edgeRate = (edge: ServiceMapEdge): string | null =>
    edge.callCount !== undefined
      ? formatRps(edge.callCount / windowSeconds)
      : edge.bytesTotal !== undefined
        ? formatBps(edge.bytesTotal / windowSeconds)
        : null;

  const openEdgeFromNode = (edge: ServiceMapEdge) => {
    setEdgeBackNodeId(selectedNodeId);
    setSelectedEdgeKey(edgeKey(edge));
  };

  const rangeLabel = isLive
    ? 'live'
    : timeRange
      ? `${describeSpan(timeRange.to.valueOf() - timeRange.from.valueOf())} range`
      : null;
  const subtitle = [
    'Built from trace spans and network flows',
    rangeLabel,
    hasLoaded && !error ? `${graphNodes.length} nodes` : null,
  ]
    .filter(Boolean)
    .join(' · ');

  const legend =
    colorMode === 'health' ? (
      <>
        <Swatch color={canvasColors.ok} label="healthy" />
        <Swatch color={canvasColors.warn} label="degraded (1%+ errors or p95 800 ms+)" />
        <Swatch color={canvasColors.bad} label="failing (5%+ errors)" />
        <Swatch color={OTHER_NODE_COLOR} label="no data" />
      </>
    ) : colorMode === 'errors' ? (
      <>
        <Swatch color={canvasColors.ok} label="under 1% errors" />
        <Swatch color={canvasColors.warn} label="1–5%" />
        <Swatch color={canvasColors.bad} label="over 5%" />
      </>
    ) : (
      <>
        <Swatch color={latencyColor(0)} label="0 ms" />
        <Swatch color={latencyColor(200)} label="200 ms" />
        <Swatch color={latencyColor(800)} label="800 ms+ avg" />
      </>
    );

  const linkList = (title: string, links: ServiceMapEdge[], arrow: string, empty: string) => (
    <div className="flex flex-col gap-1">
      <strong className="text-[13px] font-semibold text-gray-800 dark:text-gray-100">
        {title}
      </strong>
      {links.length === 0 && (
        <span className="text-xs text-gray-500 dark:text-gray-400">{empty}</span>
      )}
      {links.map((link) => {
        const other = arrow === '←' ? link.src : link.dst;
        const metric = edgeMetric(link);
        const rate = edgeRate(link);
        return (
          <button
            key={edgeKey(link)}
            type="button"
            onClick={() => openEdgeFromNode(link)}
            className="flex cursor-pointer justify-between gap-2 rounded-md border-0 bg-transparent px-1.5 py-1 text-left text-[13px] text-gray-800 hover:bg-gray-100 dark:text-gray-100 dark:hover:bg-gray-700/60"
          >
            <span className="min-w-0 truncate">
              {arrow} {other}
            </span>
            <span className="shrink-0 whitespace-nowrap">
              {rate && <span className="text-gray-500 dark:text-gray-400">{rate}</span>}
              {metric && (
                <strong className="ml-1.5 font-semibold" style={{ color: metric.color }}>
                  {metric.text}
                </strong>
              )}
            </span>
          </button>
        );
      })}
    </div>
  );

  const panelHeader = (title: string, subtitleText: string, extra?: React.ReactNode) => (
    <div className="flex items-start justify-between gap-2">
      <div className="min-w-0">
        <h2 className="m-0 text-[15px] font-semibold break-words text-gray-900 dark:text-gray-50">
          {title}
        </h2>
        <p className="m-0 mt-0.5 text-xs text-gray-500 dark:text-gray-400">{subtitleText}</p>
      </div>
      <span className="flex shrink-0 items-center gap-1.5">
        {extra}
        <Tooltip title="Close">
          <Button size="small" icon={<CloseOutlined />} aria-label="Close" onClick={closePanel} />
        </Tooltip>
      </span>
    </div>
  );

  const sourcesDescription = (sources: ServiceMapNode['sources']) =>
    sources.includes('spans') && sources.includes('network')
      ? 'Seen in trace spans and network flows'
      : sources.includes('spans')
        ? 'Seen in trace spans'
        : sources.includes('network')
          ? 'Seen in network flows only'
          : 'No sources';

  const healthPill = (health: ServiceMapHealth | undefined) => (
    <span
      className="rounded-full px-2 py-0.5 text-xs font-medium"
      style={
        health
          ? {
              color: canvasColors[HEALTH_CANVAS_KEY[health]],
              background: `color-mix(in srgb, ${canvasColors[HEALTH_CANVAS_KEY[health]]} 14%, transparent)`,
            }
          : { color: 'var(--v2-muted)', background: 'var(--v2-panel2)' }
      }
    >
      {health ? HEALTH_LABEL[health] : 'No data'}
    </span>
  );

  const redTiles = (node: GraphNode) => {
    const isService = node.nodeType === 'service' || node.name === OTHER_NODE_NAME;
    const tiles = [
      {
        label: isService ? 'Requests' : 'Calls',
        value: node.requests === undefined ? 'n/a' : formatPerMinute(node.requests / windowSeconds),
        color: undefined,
      },
      {
        label: 'Errors',
        value: node.errorRate === undefined ? 'n/a' : formatErrorRate(node.errorRate),
        color: node.errorRate === undefined ? undefined : canvasColors[errorBand(node.errorRate)],
      },
      {
        label: 'p95',
        value: node.p95LatencyMs === undefined ? 'n/a' : formatMs(node.p95LatencyMs),
        color: node.p95LatencyMs === undefined ? undefined : latencyColor(node.p95LatencyMs),
      },
    ];
    return (
      <div className="grid grid-cols-3 gap-2" aria-label="Requests, errors and p95">
        {tiles.map((tile) => (
          <span
            key={tile.label}
            className="flex min-w-0 flex-col gap-0.5 rounded-xl bg-[var(--v2-panel2)] px-2.5 py-2"
          >
            <span className="text-xs text-gray-500 dark:text-gray-400">{tile.label}</span>
            <strong
              className="truncate text-base font-semibold text-gray-900 dark:text-gray-50"
              style={tile.color ? { color: tile.color } : undefined}
            >
              {tile.value}
            </strong>
          </span>
        ))}
      </div>
    );
  };

  const nodeSubtitle = (node: GraphNode) => {
    if (node.name === OTHER_NODE_NAME) return 'Lower-traffic services';
    const sources = sourcesDescription(node.sources);
    return node.nodeType ? `${NODE_TYPE_DESCRIPTION[node.nodeType]} · ${sources}` : sources;
  };

  const nodePanel = selectedNode && (
    <>
      {panelHeader(
        selectedNode.name,
        nodeSubtitle(selectedNode),
        selectedNode.name === OTHER_NODE_NAME ? undefined : healthPill(selectedNode.health),
      )}
      <dl className="m-0 grid grid-cols-[auto_minmax(0,1fr)] gap-x-3 gap-y-1 text-sm">
        <Field label="Sources:">{selectedNode.sources.join(', ') || 'none'}</Field>
        <Field label="Traffic weight:">
          {selectedNode.traffic.toLocaleString(undefined, { maximumFractionDigits: 1 })}
        </Field>
      </dl>
      {selectedNode.requests !== undefined && redTiles(selectedNode)}
      {(selectedNode.namespace || selectedNode.deployment || selectedNode.node) && (
        <div className="flex flex-col gap-1.5 rounded-xl bg-[var(--v2-panel2)] px-3 py-2.5 text-sm">
          <strong className="text-[13px] font-semibold">Kubernetes</strong>
          <dl className="m-0 grid grid-cols-[auto_minmax(0,1fr)] gap-x-3 gap-y-1">
            {selectedNode.namespace && (
              <Field label="Namespace:">
                <span className="font-mono text-[12.5px]">{selectedNode.namespace}</span>
              </Field>
            )}
            {selectedNode.deployment && (
              <Field label="Deployment:">
                <span className="font-mono text-[12.5px]">{selectedNode.deployment}</span>
              </Field>
            )}
            {selectedNode.node && (
              <Field label="Node:">
                <span className="font-mono text-[12.5px]">{selectedNode.node}</span>
              </Field>
            )}
          </dl>
        </div>
      )}
      {selectedNode.name !== OTHER_NODE_NAME &&
        !selectedNode.namespace &&
        !selectedNode.deployment &&
        !selectedNode.node && (
          <div className="text-xs text-gray-500 dark:text-gray-400">
            No Kubernetes metadata (network-only service, or ingested without k8sattributes
            enrichment).
          </div>
        )}
      {selectedNode.name === OTHER_NODE_NAME && (
        <div className="rounded-xl border border-dashed border-gray-200 p-2.5 text-xs text-gray-500 dark:border-gray-700 dark:text-gray-400">
          Collapsed bucket of lower-traffic services, not a single real service.
        </div>
      )}
      {(() => {
        const { inbound, outbound } = linksOf(selectedNode.id);
        return (
          <div className="flex flex-col gap-2">
            {linkList('Calls into it', inbound, '←', 'No inbound calls seen in this window.')}
            {linkList('Calls it makes', outbound, '→', 'No downstream calls.')}
          </div>
        );
      })()}
      {onViewTraces &&
        selectedNode.name !== OTHER_NODE_NAME &&
        selectedNode.sources.includes('spans') && (
          <div>
            <Button type="primary" size="small" onClick={() => onViewTraces(selectedNode.name)}>
              View traces
            </Button>
          </div>
        )}
    </>
  );

  const edgePanel = selectedEdge && (
    <>
      {panelHeader(
        `${selectedEdge.edge.src} → ${selectedEdge.edge.dst}`,
        'One dependency',
        backNode && (
          <Button size="small" onClick={() => setSelectedEdgeKey(null)}>
            ← {backNode.name}
          </Button>
        ),
      )}
      <dl className="m-0 grid grid-cols-[auto_minmax(0,1fr)] gap-x-3 gap-y-1 text-sm">
        <Field label="Sources:">{selectedEdge.edge.sources.join(', ')}</Field>
        {selectedEdge.edge.callCount !== undefined && (
          <Field label="Calls:">
            {selectedEdge.edge.callCount.toLocaleString()} (
            {formatRps(selectedEdge.edge.callCount / windowSeconds)})
          </Field>
        )}
        {selectedEdge.edge.avgLatencyMs !== undefined && (
          <Field label="Avg latency:">
            <span style={{ color: latencyColor(selectedEdge.edge.avgLatencyMs) }}>
              {selectedEdge.edge.avgLatencyMs.toFixed(1)} ms
            </span>
          </Field>
        )}
        {selectedEdge.edge.errorRate !== undefined && (
          <Field label="Error rate:">
            <span style={{ color: canvasColors[errorBand(selectedEdge.edge.errorRate)] }}>
              {formatErrorRate(selectedEdge.edge.errorRate)}
            </span>
          </Field>
        )}
        {selectedEdge.edge.bytesTotal !== undefined && (
          <Field label="Bytes:">
            {formatBytes(selectedEdge.edge.bytesTotal)} (
            {formatBps(selectedEdge.edge.bytesTotal / windowSeconds)})
          </Field>
        )}
      </dl>
    </>
  );

  const headerActions = (
    <>
      {isLive ? (
        <div className="flex items-center gap-2 text-sm">
          <span className="text-[var(--v2-accent)]">
            Live - last {LIVE_WINDOW_SECONDS}s, refreshing every {LIVE_POLL_INTERVAL_MS / 1000}s
          </span>
          <Tooltip title={isPaused ? 'Resume' : 'Pause'}>
            <Button
              size="small"
              aria-label={isPaused ? 'Resume' : 'Pause'}
              icon={isPaused ? <CaretRightOutlined /> : <PauseOutlined />}
              onClick={() => setIsPaused((p) => !p)}
            />
          </Tooltip>
          {lastUpdated && (
            <span className="text-gray-400 dark:text-gray-500">
              {isPaused ? 'Paused · ' : ''}Updated {lastUpdated.toLocaleTimeString()}
            </span>
          )}
        </div>
      ) : (
        <TimeRangePickerComponent compact onChange={setTimeRange} />
      )}
      <label className="flex cursor-pointer items-center gap-2 text-sm">
        <Switch
          checked={isLive}
          onChange={(checked) => {
            setIsLive(checked);
            setIsPaused(false);
          }}
        />
        <span>Live</span>
      </label>
      {graphNodes.length > 0 && (
        <Tooltip title="Forget dragged positions and lay the nodes out again">
          <Button size="small" onClick={resetLayout}>
            Reset layout
          </Button>
        </Tooltip>
      )}
      {availableNamespaces.length > 0 && (
        <Select
          size="small"
          mode="multiple"
          allowClear
          maxTagCount="responsive"
          placeholder="All namespaces"
          className="min-w-56"
          value={namespaceFilter}
          onChange={(value: string[]) => setNamespaceFilter(value)}
          options={availableNamespaces.map((ns) => ({
            value: ns,
            label: ns === UNKNOWN_NAMESPACE ? 'Unknown / network-only' : ns,
          }))}
        />
      )}
      <Tooltip title="Colour the call edges by average latency or error rate, or the nodes by health">
        <Segmented<ColorMode>
          size="small"
          aria-label="Colour by"
          value={colorMode}
          onChange={setColorMode}
          options={[
            { value: 'latency', label: 'Latency' },
            { value: 'errors', label: 'Errors' },
            { value: 'health', label: 'Health' },
          ]}
        />
      </Tooltip>
    </>
  );

  return (
    <div className="flex grow flex-col gap-4 overflow-auto" style={{ height: contentHeight }}>
      <PageHeader title="Service map" subtitle={subtitle} actions={headerActions} />

      <div className="flex flex-wrap items-start gap-4">
        <Card
          className="flex-[999_1_640px]"
          title="Dependencies"
          subtitle={`Width is traffic; colour is ${
            colorMode === 'health'
              ? 'node health (edges: error rate)'
              : colorMode === 'errors'
                ? 'the error rate'
                : 'average latency'
          }; arrows point in the call direction`}
          extra={
            <div className="flex flex-wrap items-center gap-x-3 gap-y-1 text-xs text-gray-500 dark:text-gray-400">
              {legend}
              <LineSample dashed={false} label="seen in spans" />
              <LineSample dashed label="network-only" />
            </div>
          }
          bodyClassName="flex flex-col gap-3"
        >
          {error && (
            <div className="flex h-64 items-center justify-center text-sm text-red-500">
              {error}
            </div>
          )}

          {!error && !isLive && !timeRange && (
            <div className="flex justify-center py-8">
              <Spin indicator={<LoadingOutlined spin />} />
            </div>
          )}

          {!error && (isLive || timeRange) && isLoading && graphNodes.length === 0 && (
            <div className="flex justify-center py-8">
              <Spin indicator={<LoadingOutlined spin />} />
            </div>
          )}

          {!error && hasLoaded && !isLoading && graphNodes.length === 0 && (
            <Empty description="No service map data in this time range" />
          )}

          {hasGraph && (
            <>
              <div
                ref={containerRef}
                className="w-full"
                style={{ height: Math.max(contentHeight - 300, 320) }}
              />
              <div role="group" aria-label="Pick a service" className="flex flex-wrap gap-1.5">
                {graphNodes.map((node) => {
                  const isSelected = node.id === selectedNodeId && !selectedEdge;
                  return (
                    <button
                      key={node.id}
                      type="button"
                      aria-pressed={isSelected}
                      onClick={() => {
                        setSelectedNodeId(node.id);
                        setSelectedEdgeKey(null);
                        setEdgeBackNodeId(null);
                      }}
                      className={`inline-flex cursor-pointer items-center gap-1.5 rounded-full border bg-transparent px-2.5 py-0.5 text-xs text-gray-800 dark:text-gray-100 ${
                        isSelected
                          ? 'border-[var(--v2-accent)]'
                          : 'border-gray-200 hover:border-gray-300 dark:border-gray-700 dark:hover:border-gray-600'
                      }`}
                    >
                      <span
                        className="h-2 w-2 rounded-full"
                        style={{ backgroundColor: node.itemStyle.color }}
                      />
                      {node.name}
                    </button>
                  );
                })}
              </div>
              <span className="text-xs text-gray-500 dark:text-gray-400">
                Drag nodes to move them; scroll to zoom. Node size follows traffic. Click a node, a
                line or a name above for details.
              </span>
            </>
          )}
        </Card>

        {(selectedEdge || selectedNode) && (
          <aside
            aria-label="Details"
            className="flex min-w-0 flex-[1_1_340px] flex-col gap-3 rounded-2xl border border-gray-200 bg-white p-5 text-sm dark:border-gray-700 dark:bg-gray-800"
          >
            {selectedEdge ? edgePanel : nodePanel}
          </aside>
        )}
      </div>
    </div>
  );
};
