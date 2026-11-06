import { Button, Drawer, Empty, Select, Spin, Switch, Tooltip } from 'antd';
import type { EChartsOption } from 'echarts';
import type { ECharts } from 'echarts/core';
import type { GraphSeriesOption } from 'echarts/types/dist/shared';
import type { CallbackDataParams } from 'echarts/types/dist/shared';
import React, { useEffect, useMemo, useRef, useState } from 'react';

import {
  type ServiceMapEdge,
  type ServiceMapNode,
  serviceMapApi,
} from '../../../entity/serviceMap';
import { escapeHtml, useVisibleInterval } from '../../../shared/lib';
import { useTheme } from '../../../shared/theme';
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

const formatBytes = (bytes: number): string => {
  if (!bytes || bytes <= 0) return '0 B';
  const units = ['B', 'KB', 'MB', 'GB', 'TB'];
  const exponent = Math.min(Math.floor(Math.log(bytes) / Math.log(1024)), units.length - 1);
  const value = bytes / 1024 ** exponent;
  return `${value.toFixed(value >= 10 || exponent === 0 ? 0 : 1)} ${units[exponent]}`;
};

const formatErrorRate = (errorRate: number): string => `${(errorRate * 100).toFixed(1)}%`;
const formatRps = (rps: number): string => `${rps < 1 ? rps.toFixed(2) : rps.toFixed(1)} req/s`;
const formatBps = (bytesPerSecond: number): string => `${formatBytes(bytesPerSecond)}/s`;

// Traffic weight for sizing, as rates so live and historical windows look
// comparable. Bytes/sec is scaled to KB/s to sit near call rates (a heuristic).
const edgeWeight = (edge: ServiceMapEdge, windowSeconds: number): number =>
  (edge.callCount ?? 0) / windowSeconds + (edge.bytesTotal ?? 0) / windowSeconds / 1024;

const clamp = (value: number, min: number, max: number): number =>
  Math.max(min, Math.min(max, value));

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

function buildGraphData(nodes: ServiceMapNode[], edges: ServiceMapEdge[], windowSeconds: number) {
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

    return {
      id: node.name,
      name: node.name,
      symbolSize,
      itemStyle: isOther
        ? { color: OTHER_NODE_COLOR, borderColor: '#4b5563', borderWidth: 2, borderType: 'dashed' }
        : { color: colorForService(node.name) },
      sources: node.sources,
      traffic,
      namespace: node.namespace,
      deployment: node.deployment,
      node: node.node,
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

    return {
      source: edge.src,
      target: edge.dst,
      lineStyle: {
        // Edges seen in spans draw solid; network-only edges are a weaker inference.
        type: hasSpans ? 'solid' : 'dashed',
        width: hasSpans && hasNetwork ? width + 1 : width,
        // Spans edges are colored by latency; network-only edges have none.
        color:
          hasSpans && edge.avgLatencyMs !== undefined ? latencyColor(edge.avgLatencyMs) : undefined,
      },
      edge,
    };
  });

  return { graphNodes, graphEdges };
}

export const ServiceMapComponent = ({
  projectId,
  contentHeight,
  onViewTraces,
}: Props): React.JSX.Element => {
  const { theme } = useTheme();
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

  const [selectedNode, setSelectedNode] = useState<GraphNode | null>(null);
  const [selectedEdge, setSelectedEdge] = useState<GraphEdge | null>(null);

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
        setWindowSeconds(Math.max(1, (to.valueOf() - from.valueOf()) / 1000));
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

  const { graphNodes, graphEdges } = useMemo(
    () => buildGraphData(filteredNodes, filteredEdges, windowSeconds),
    [filteredNodes, filteredEdges, windowSeconds],
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

    const isDark = theme === 'dark';
    const option: EChartsOption = {
      tooltip: {
        backgroundColor: isDark ? '#1f2937' : '#ffffff',
        borderColor: isDark ? '#4b5563' : '#e5e7eb',
        textStyle: { color: isDark ? '#e5e7eb' : '#374151' },
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
            color: isDark ? '#e5e7eb' : '#374151',
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
            color: isDark ? '#6b7280' : '#9ca3af',
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
        setSelectedNode(params.data as GraphNode);
        setSelectedEdge(null);
      } else if (params.dataType === 'edge') {
        setSelectedEdge(params.data as GraphEdge);
        setSelectedNode(null);
      }
    });

    const handleResize = () => chart.resize();
    window.addEventListener('resize', handleResize);
    return () => {
      window.removeEventListener('resize', handleResize);
      zr.off('mouseup', capturePositions);
    };
  }, [graphNodes, graphEdges, theme, windowSeconds, layoutVersion]);

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

  const closeDrawer = () => {
    setSelectedNode(null);
    setSelectedEdge(null);
  };

  return (
    <div
      className="grow overflow-auto rounded bg-white p-4 shadow dark:bg-gray-800"
      style={{ height: contentHeight }}
    >
      <div className="mb-4 flex flex-wrap items-center gap-3">
        {isLive ? (
          <div className="flex items-center gap-2 text-sm text-gray-600 dark:text-gray-300">
            <span>
              Live - last {LIVE_WINDOW_SECONDS}s, refreshing every {LIVE_POLL_INTERVAL_MS / 1000}s
            </span>
            <Tooltip title={isPaused ? 'Resume' : 'Pause'}>
              <Button size="small" onClick={() => setIsPaused((p) => !p)}>
                {isPaused ? '▶' : '⏸'}
              </Button>
            </Tooltip>
            {lastUpdated && (
              <span className="text-gray-400 dark:text-gray-500">
                Updated {lastUpdated.toLocaleTimeString()}
              </span>
            )}
          </div>
        ) : (
          <TimeRangePickerComponent compact onChange={setTimeRange} />
        )}
        <div className="flex items-center gap-2 text-sm">
          <Switch
            checked={isLive}
            onChange={(checked) => {
              setIsLive(checked);
              setIsPaused(false);
            }}
          />
          <span>Live</span>
        </div>
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
      </div>

      {error && (
        <div className="flex h-64 items-center justify-center text-sm text-red-500">{error}</div>
      )}

      {!error && !isLive && !timeRange && (
        <div className="flex justify-center py-8">
          <Spin />
        </div>
      )}

      {!error && (isLive || timeRange) && isLoading && graphNodes.length === 0 && (
        <div className="flex justify-center py-8">
          <Spin />
        </div>
      )}

      {!error && hasLoaded && !isLoading && graphNodes.length === 0 && (
        <Empty description="No service map data in this time range" />
      )}

      {!error && graphNodes.length > 0 && (
        <div
          ref={containerRef}
          className="h-full w-full"
          style={{ height: Math.max(contentHeight - 100, 300) }}
        />
      )}

      <Drawer
        title={
          selectedNode
            ? selectedNode.name
            : selectedEdge
              ? `${selectedEdge.edge.src} → ${selectedEdge.edge.dst}`
              : ''
        }
        open={!!selectedNode || !!selectedEdge}
        onClose={closeDrawer}
        placement="right"
      >
        {selectedNode && (
          <div className="space-y-2 text-sm">
            <div>
              <span className="font-medium">Sources: </span>
              {selectedNode.sources.join(', ') || 'none'}
            </div>
            <div>
              <span className="font-medium">Traffic weight: </span>
              {selectedNode.traffic.toLocaleString(undefined, { maximumFractionDigits: 1 })}
            </div>
            {(selectedNode.namespace || selectedNode.deployment || selectedNode.node) && (
              <div className="rounded border border-gray-200 p-2 dark:border-gray-700">
                <div className="mb-1 font-medium">Kubernetes</div>
                {selectedNode.namespace && (
                  <div>
                    <span className="text-gray-500 dark:text-gray-400">Namespace: </span>
                    {selectedNode.namespace}
                  </div>
                )}
                {selectedNode.deployment && (
                  <div>
                    <span className="text-gray-500 dark:text-gray-400">Deployment: </span>
                    {selectedNode.deployment}
                  </div>
                )}
                {selectedNode.node && (
                  <div>
                    <span className="text-gray-500 dark:text-gray-400">Node: </span>
                    {selectedNode.node}
                  </div>
                )}
              </div>
            )}
            {selectedNode.name !== OTHER_NODE_NAME &&
              !selectedNode.namespace &&
              !selectedNode.deployment &&
              !selectedNode.node && (
                <div className="text-gray-500 dark:text-gray-400">
                  No Kubernetes metadata (network-only service, or ingested without k8sattributes
                  enrichment).
                </div>
              )}
            {selectedNode.name === OTHER_NODE_NAME && (
              <div className="text-gray-500 dark:text-gray-400">
                Collapsed bucket of lower-traffic services, not a single real service.
              </div>
            )}
            {onViewTraces &&
              selectedNode.name !== OTHER_NODE_NAME &&
              selectedNode.sources.includes('spans') && (
                <Button type="primary" size="small" onClick={() => onViewTraces(selectedNode.name)}>
                  View traces
                </Button>
              )}
          </div>
        )}

        {selectedEdge && (
          <div className="space-y-2 text-sm">
            <div>
              <span className="font-medium">Sources: </span>
              {selectedEdge.edge.sources.join(', ')}
            </div>
            {selectedEdge.edge.callCount !== undefined && (
              <div>
                <span className="font-medium">Calls: </span>
                {selectedEdge.edge.callCount.toLocaleString()} (
                {formatRps(selectedEdge.edge.callCount / windowSeconds)})
              </div>
            )}
            {selectedEdge.edge.avgLatencyMs !== undefined && (
              <div>
                <span className="font-medium">Avg latency: </span>
                {selectedEdge.edge.avgLatencyMs.toFixed(1)} ms
              </div>
            )}
            {selectedEdge.edge.errorRate !== undefined && (
              <div>
                <span className="font-medium">Error rate: </span>
                {formatErrorRate(selectedEdge.edge.errorRate)}
              </div>
            )}
            {selectedEdge.edge.bytesTotal !== undefined && (
              <div>
                <span className="font-medium">Bytes: </span>
                {formatBytes(selectedEdge.edge.bytesTotal)} (
                {formatBps(selectedEdge.edge.bytesTotal / windowSeconds)})
              </div>
            )}
          </div>
        )}
      </Drawer>
    </div>
  );
};
