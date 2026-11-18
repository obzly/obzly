// The project's landing page: one number per signal with its change, linking to the view behind it.
// Every panel loads on its own (in parallel) and shows "n/a" when its request fails.
import {
  AlertOutlined,
  ClockCircleOutlined,
  ReloadOutlined,
  UnorderedListOutlined,
  WarningOutlined,
} from '@ant-design/icons';
import { Button, Segmented, Tooltip } from 'antd';
import { useMemo, useState } from 'react';

import { IS_CONTROL_PLANE } from '../../../constants';
import { aiBudgetsApi } from '../../../entity/aiUsage';
import { alertingApi } from '../../../entity/alerting';
import { correlationApi } from '../../../entity/correlation';
import { dashboardApi } from '../../../entity/dashboards';
import { diskApi } from '../../../entity/disk';
import { inventoryApi } from '../../../entity/inventory';
import { projectApi } from '../../../entity/projects';
import { queryApi } from '../../../entity/query';
import { maintenanceApi, syntheticsApi } from '../../../entity/synthetics';
import { traceApi } from '../../../entity/traces';
import { useVisibleInterval } from '../../../shared/lib';
import { useTheme } from '../../../shared/theme';
import { KpiTile, PageHeader } from '../../../shared/ui/v2';
import { IngestionStatusComponent } from '../../projects';
import {
  type GaugeModel,
  aiBudgetGauge,
  alertNoiseGauge,
  hotStorageGauge,
  logQuotaGauge,
} from '../lib/capacity';
import {
  OVERVIEW_RANGES,
  type OverviewRange,
  RANGE_TEXT,
  errorRate,
  formatMs,
  formatRate,
  logVolume,
  overviewWindow,
  percentChange,
  pointChange,
  sessionsPerBucket,
  summarizeIncidents,
} from '../lib/overview';
import { useLoad } from '../lib/useLoad';
import { CapacityPanels } from './CapacityPanels';
import { IncidentsPanel } from './IncidentsPanel';
import { InfrastructurePanel } from './InfrastructurePanel';
import { SyntheticsPanel, SyntheticsUnavailable } from './SyntheticsPanel';
import { RequestsPanel, ServiceErrorsPanel } from './TracePanels';

/** The pages the Overview links to. */
export type OverviewTarget =
  | 'search'
  | 'traces'
  | 'service-map'
  | 'incidents'
  | 'alerts'
  | 'ai-observability'
  | 'synthetics'
  | 'infrastructure'
  | 'settings'
  | 'data-sources';

interface Props {
  projectId: string;
  projectName: string;
  /** The Site the project is on ('' on a standalone Site). */
  siteId: string;
  contentHeight: number;
  /** Opens a page; serviceName filters Traces to one service. */
  onNavigate: (target: OverviewTarget, options?: { serviceName?: string }) => void;
}

const RANGE_STORAGE_KEY = 'obzly-overview-range';
const REFRESH_MS = 5 * 60_000;
const WEEK_MS = 7 * 24 * 60 * 60_000;
// The session list's own cap.
const SESSION_LIMIT = 500;
const MAINTENANCE_SPAN_MS = 25 * 60 * 60_000;

const readRange = (): OverviewRange => {
  try {
    const stored = localStorage.getItem(RANGE_STORAGE_KEY);
    return OVERVIEW_RANGES.includes(stored as OverviewRange) ? (stored as OverviewRange) : '24h';
  } catch {
    return '24h';
  }
};

const iso = (ms: number) => new Date(ms).toISOString();

export const OverviewComponent = ({
  projectId,
  projectName,
  siteId,
  contentHeight,
  onNavigate,
}: Props) => {
  const { chartColors, status } = useTheme();
  const [range, setRange] = useState<OverviewRange>(readRange);
  const [refresh, setRefresh] = useState(0);

  useVisibleInterval(() => setRefresh((value) => value + 1), REFRESH_MS);

  const period = useMemo(() => overviewWindow(range), [range, refresh]);
  const timeRange = { from: iso(period.from), to: iso(period.to) };
  const deps = [projectId, period];

  const logs = useLoad(
    (signal) =>
      dashboardApi.executePanelQuery(
        projectId,
        {
          panel: {
            id: 'overview-log-volume',
            title: '',
            type: 'bar',
            source: 'logs',
            query: null,
            bucketMs: period.bucketMs,
            gridPos: { x: 0, y: 0, w: 1, h: 1 },
          },
          timeRange: { from: iso(period.previousFrom), to: iso(period.to) },
        },
        signal,
      ),
    deps,
  );
  const summary = useLoad(
    (signal) => traceApi.getTraceSummary(projectId, { timeRange }, signal),
    deps,
  );
  const services = useLoad(
    (signal) => traceApi.getServiceErrors(projectId, { timeRange, limit: 6 }, signal),
    deps,
  );
  // At least the last 7 days (the incidents panel), and the whole range for the tile.
  const sessions = useLoad(
    async () =>
      (
        await correlationApi.listSessions(
          projectId,
          iso(Math.min(period.from, period.to - WEEK_MS)),
          iso(period.to),
          SESSION_LIMIT,
        )
      ).sessions,
    deps,
  );
  const alerts = useLoad(() => alertingApi.getSummary(projectId), deps);
  const inventory = useLoad((signal) => inventoryApi.summary(siteId, signal), [siteId, refresh]);

  const aiBudget = useLoad<GaugeModel>(async () => {
    const [budgets, statuses] = await Promise.all([
      aiBudgetsApi.listBudgets(projectId),
      aiBudgetsApi.getStatus(projectId),
    ]);
    return aiBudgetGauge(budgets, statuses);
  }, deps);
  const logQuota = useLoad<GaugeModel>(async () => {
    const [project, stats] = await Promise.all([
      projectApi.getProject(projectId),
      queryApi.getProjectStats(projectId),
    ]);
    return logQuotaGauge(project, stats);
  }, deps);
  const hotStorage = useLoad<GaugeModel>(async () => {
    const [logStats, spanStats, disk] = await Promise.all([
      queryApi.getProjectStats(projectId),
      traceApi.getProjectStats(projectId).catch(() => undefined),
      diskApi.getDiskUsage(),
    ]);
    return hotStorageGauge(logStats, spanStats, disk);
  }, deps);
  const alertNoise = useLoad<GaugeModel>(
    async () => alertNoiseGauge(await alertingApi.getRuleActivity(projectId)),
    deps,
  );

  const synthetics = useLoad(
    IS_CONTROL_PLANE
      ? async () => {
          const now = Date.now();
          const [checks, maintenance] = await Promise.all([
            syntheticsApi.listChecks(),
            maintenanceApi
              .occurrences(new Date(now - MAINTENANCE_SPAN_MS), new Date(now + 60 * 60_000))
              .catch(() => []),
          ]);
          return {
            checks: checks.filter(
              (check) =>
                check.destination.projectId === projectId &&
                (!siteId || check.destination.siteId === siteId),
            ),
            maintenance,
          };
        }
      : undefined,
    [projectId, siteId, refresh],
  );

  const volume = useMemo(
    () => (logs.data ? logVolume(logs.data.buckets, period) : undefined),
    [logs.data, period],
  );
  const inRange = useMemo(
    () => sessions.data?.filter((session) => Date.parse(session.windowEnd) >= period.from),
    [sessions.data, period],
  );

  const current = summary.data?.current;
  const previous = summary.data?.previous;
  const rateNow = errorRate(current?.errors, current?.count);
  const ratePrevious = errorRate(previous?.errors, previous?.count);
  const p95 = formatMs(current?.p95Ms);
  const leader = services.data?.services.find((service) => service.errors > 0);
  const rate = volume ? formatRate(volume.rate) : undefined;
  const incidents = inRange ? summarizeIncidents(inRange, period.to) : undefined;
  const opened = inRange?.filter((session) => Date.parse(session.windowStart) >= period.from);

  const loadingValue = (loading: boolean) => (loading ? '…' : 'n/a');

  return (
    <div
      className="ml-3 flex w-full flex-col gap-5 overflow-y-auto pr-1 pb-4"
      style={{ height: contentHeight }}
    >
      <div className="rounded-2xl border border-gray-200 bg-white p-5 dark:border-gray-700 dark:bg-gray-800">
        <PageHeader
          title="Overview"
          subtitle={`${projectName} · ${RANGE_TEXT[range]} · every number links to the view behind it`}
          actions={
            <>
              <Segmented
                aria-label="Time range"
                value={range}
                onChange={(value) => {
                  const next = value as OverviewRange;
                  setRange(next);
                  try {
                    localStorage.setItem(RANGE_STORAGE_KEY, next);
                  } catch {
                    /* the range resets to 24 h next time */
                  }
                }}
                options={OVERVIEW_RANGES}
              />
              <Tooltip title="Refresh">
                <Button
                  aria-label="Refresh"
                  icon={<ReloadOutlined />}
                  onClick={() => setRefresh((value) => value + 1)}
                />
              </Tooltip>
            </>
          }
        />
      </div>

      <section
        aria-label="Key numbers"
        className="grid grid-cols-[repeat(auto-fit,minmax(220px,1fr))] gap-4"
      >
        <KpiTile
          label="Log lines"
          value={rate?.value ?? loadingValue(logs.loading)}
          unit={rate?.unit}
          icon={<UnorderedListOutlined />}
          // Volume is neither good nor bad by itself.
          change={(() => {
            const change = percentChange(volume?.rate, volume?.previousRate, true);
            return change && { text: change.text, tone: 'neutral' as const };
          })()}
          note={
            volume
              ? `${volume.total.toLocaleString()} lines · vs previous ${range}`
              : logs.error && `Couldn't load: ${logs.error}`
          }
          trend={volume?.trend}
          color={chartColors[0]}
          onClick={() => onNavigate('search')}
        />
        <KpiTile
          label="Error rate"
          value={rateNow === undefined ? loadingValue(summary.loading) : rateNow.toFixed(1)}
          unit={rateNow === undefined ? undefined : '%'}
          icon={<WarningOutlined />}
          change={pointChange(rateNow, ratePrevious)}
          note={
            summary.error
              ? `Couldn't load: ${summary.error}`
              : leader
                ? `${leader.serviceName} leads · trace roots`
                : 'Failed trace roots'
          }
          trend={summary.data?.errorRateTrend}
          color={status.warn}
          onClick={() => onNavigate('traces')}
        />
        <KpiTile
          label="p95 latency"
          value={p95?.value ?? loadingValue(summary.loading)}
          unit={p95?.unit}
          icon={<ClockCircleOutlined />}
          change={percentChange(current?.p95Ms ?? undefined, previous?.p95Ms ?? undefined, false)}
          note={summary.error ? `Couldn't load: ${summary.error}` : 'Trace roots'}
          trend={summary.data?.p95Trend}
          color={chartColors[1 % chartColors.length]}
          onClick={() => onNavigate('traces')}
        />
        <KpiTile
          label="Ongoing incidents"
          value={
            incidents ? incidents.counts.ongoing.toLocaleString() : loadingValue(sessions.loading)
          }
          icon={<AlertOutlined />}
          change={
            opened
              ? { text: `${opened.length} new`, tone: opened.length > 0 ? 'bad' : 'neutral' }
              : undefined
          }
          note={
            sessions.error
              ? `Couldn't load: ${sessions.error}`
              : [
                  incidents && `${incidents.total} in the ${RANGE_TEXT[range]}`,
                  alerts.data
                    ? `${alerts.data.firing} alert${alerts.data.firing === 1 ? '' : 's'} firing`
                    : alerts.error && 'alerts n/a',
                ]
                  .filter(Boolean)
                  .join(' · ')
          }
          trend={inRange ? sessionsPerBucket(inRange, period) : undefined}
          color={chartColors[4 % chartColors.length]}
          onClick={() => onNavigate('incidents')}
        />
      </section>

      <IngestionStatusComponent
        projectId={projectId}
        onAddSource={() => onNavigate('data-sources')}
      />

      <div className="grid gap-4 lg:grid-cols-[minmax(0,1fr)_minmax(0,1.6fr)]">
        <ServiceErrorsPanel
          state={services}
          onOpenService={(serviceName) => onNavigate('traces', { serviceName })}
          onOpenServiceMap={() => onNavigate('service-map')}
        />
        <RequestsPanel
          state={summary}
          sessions={inRange}
          from={period.from}
          range={range}
          onOpen={() => onNavigate('traces')}
        />
      </div>

      <CapacityPanels
        items={[
          {
            key: 'ai',
            title: 'AI spend budget',
            subtitle: 'The budget closest to its limit',
            color: chartColors[0],
            state: aiBudget,
            onOpen: () => onNavigate('ai-observability'),
          },
          {
            key: 'quota',
            title: 'Log storage quota',
            subtitle: 'Stored logs against the size limit',
            color: chartColors[1 % chartColors.length],
            state: logQuota,
            onOpen: () => onNavigate('settings'),
          },
          {
            key: 'hot',
            title: 'Hot storage',
            subtitle: 'Logs and spans still on local disk',
            color: chartColors[2 % chartColors.length],
            state: hotStorage,
          },
          {
            key: 'noise',
            title: 'Alert noise',
            subtitle: 'Notifications sent per alert firing',
            color: chartColors[3 % chartColors.length],
            state: alertNoise,
            onOpen: () => onNavigate('alerts'),
          },
        ]}
      />

      <div className="grid gap-4 xl:grid-cols-3">
        <IncidentsPanel state={sessions} now={period.to} onOpen={() => onNavigate('incidents')} />
        {IS_CONTROL_PLANE ? (
          <SyntheticsPanel
            state={synthetics}
            refresh={refresh}
            onOpen={() => onNavigate('synthetics')}
          />
        ) : (
          <SyntheticsUnavailable />
        )}
        <InfrastructurePanel
          state={inventory}
          multiSite={IS_CONTROL_PLANE}
          onOpen={() => onNavigate('infrastructure')}
        />
      </div>
    </div>
  );
};
