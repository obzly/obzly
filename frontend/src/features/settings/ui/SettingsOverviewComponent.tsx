// On a control plane, a Site that doesn't answer shows as such instead of
// failing the page.
import { CopyOutlined, LoadingOutlined, RightOutlined } from '@ant-design/icons';
import { App, Button, Progress, Spin, Table, Tooltip } from 'antd';
import dayjs from 'dayjs';
import type { EChartsOption } from 'echarts';
import { useCallback, useEffect, useMemo, useState } from 'react';

import { APP_VERSION, IS_CONTROL_PLANE, getApplicationServer } from '../../../constants';
import { notificationSettingsApi } from '../../../entity/alerting';
import type { SettingsSync } from '../../../entity/alerting';
import { auditLogApi } from '../../../entity/audit-logs/api/auditLogApi';
import type { AuditLog } from '../../../entity/audit-logs/model/AuditLog';
import type { SystemDashboard } from '../../../entity/query/model/SystemDashboard';
import { type SiteAdmin, type SiteStatus, sitesApi } from '../../../entity/sites';
import {
  type SystemUsage,
  type UpgradeStatusScope,
  type UsageSignal,
  systemApi,
} from '../../../entity/system';
import { userManagementApi } from '../../../entity/users/api/userManagementApi';
import type { UserProfile } from '../../../entity/users/model/UserProfile';
import { UserRole } from '../../../entity/users/model/UserRole';
import { copyToClipboard, useVisibleInterval } from '../../../shared/lib';
import { type ChartColors, EChart, Panel } from '../../synthetics';
import { type SettingsTab, TAB_LABEL } from '../lib/tabs';

// Release builds are numbered; a local build says dev.
const displayVersion = /^\d/.test(APP_VERSION) ? `v${APP_VERSION}` : APP_VERSION;

interface Props {
  onNavigate: (tab: SettingsTab, siteId?: string) => void;
}

type Health = 'up' | 'degraded' | 'down' | 'unknown';

interface Deployment {
  scope: UpgradeStatusScope;
  name: string;
  role: 'Control plane' | 'Site';
  siteId?: string;
  health: Health;
  message?: string;
  version?: string;
  dashboard?: SystemDashboard;
  usage?: SystemUsage;
}

interface Attention {
  text: string;
  tab?: SettingsTab;
  siteId?: string;
}

const REFRESH_MS = 60_000;
const USERS_LIMIT = 1000;
// A Site fetches shared settings every 5 minutes; three missed fetches is stale.
const SYNC_STALE_MS = 15 * 60_000;
const SIGNALS: UsageSignal[] = ['logs', 'metrics', 'traces'];
const SIGNAL_COLOR: Record<UsageSignal, string> = {
  logs: '#059669',
  metrics: '#0ea5e9',
  traces: '#8b5cf6',
};
const SITE_COLORS = ['#059669', '#0ea5e9', '#8b5cf6', '#f97316', '#ec4899', '#84cc16', '#64748b'];
const HEALTH_COLOR: Record<Health, string> = {
  up: '#10b981',
  degraded: '#f59e0b',
  down: '#ef4444',
  unknown: '#9ca3af',
};

const formatBytes = (bytes?: number): string => {
  if (bytes === undefined) return '—';
  if (bytes <= 0) return '0 B';
  const units = ['B', 'KB', 'MB', 'GB', 'TB', 'PB'];
  const exponent = Math.min(Math.floor(Math.log(bytes) / Math.log(1024)), units.length - 1);
  const value = bytes / 1024 ** exponent;
  return `${value.toFixed(value >= 10 || exponent === 0 ? 0 : 1)} ${units[exponent]}`;
};

const percent = (used: number, total: number) => (total > 0 ? (used / total) * 100 : 0);

const meter = (value: number | undefined, label: string) =>
  value === undefined ? (
    '—'
  ) : (
    <Tooltip title={label}>
      <div className="w-24">
        <Progress
          percent={Math.round(value)}
          size="small"
          strokeColor={value >= 90 ? '#dc2626' : value >= 75 ? '#d97706' : '#059669'}
        />
      </div>
    </Tooltip>
  );

export const SettingsOverviewComponent = ({ onNavigate }: Props) => {
  const { message } = App.useApp();
  const [deployments, setDeployments] = useState<Deployment[]>();
  const [users, setUsers] = useState<UserProfile[]>();
  const [sync, setSync] = useState<SettingsSync>();
  const [activity, setActivity] = useState<AuditLog[]>();
  const [refresh, setRefresh] = useState(0);

  const load = useCallback(async () => {
    const list: Deployment[] = [];
    if (IS_CONTROL_PLANE) {
      const [sites, statuses] = await Promise.all([
        sitesApi.listSitesAdmin().catch(() => [] as SiteAdmin[]),
        sitesApi.getSiteStatuses().catch(() => [] as SiteStatus[]),
      ]);
      list.push({
        scope: 'central',
        name: 'Control plane',
        role: 'Control plane',
        health: 'up',
        version: APP_VERSION,
      });
      for (const site of sites.filter((item) => item.enabled)) {
        const status = statuses.find((item) => item.siteId === site.id);
        list.push({
          scope: `site:${site.id}`,
          name: site.displayName || site.id,
          role: 'Site',
          siteId: site.id,
          health:
            status?.status === 'up' || status?.status === 'degraded' || status?.status === 'down'
              ? status.status
              : 'unknown',
          message: status?.message,
          version: status?.version,
        });
      }
    } else {
      list.push({
        scope: 'site',
        name: 'This deployment',
        role: 'Site',
        health: 'up',
        version: APP_VERSION,
      });
    }
    await Promise.all(
      list
        .filter((item) => item.role === 'Site' && item.health !== 'down')
        .map(async (item) => {
          const [dashboard, usage] = await Promise.allSettled([
            systemApi.getDashboard(item.scope),
            systemApi.getUsage(item.scope),
          ]);
          if (dashboard.status === 'fulfilled') item.dashboard = dashboard.value;
          if (usage.status === 'fulfilled') item.usage = usage.value;
        }),
    );
    setDeployments(list);
  }, []);

  useEffect(() => {
    void load();
    userManagementApi
      .getUsers({ limit: USERS_LIMIT })
      .then((response) => setUsers(response.users))
      .catch(() => setUsers([]));
    notificationSettingsApi
      .getSync()
      .then(setSync)
      .catch(() => setSync(undefined));
    auditLogApi
      .getGlobalAuditLogs({ limit: 6 })
      .then((response) => setActivity(response.auditLogs))
      .catch(() => setActivity([]));
  }, [load]);
  useVisibleInterval(() => setRefresh((value) => value + 1), REFRESH_MS);

  useEffect(() => {
    if (refresh > 0) void load();
  }, [refresh, load]);

  const sites = (deployments ?? []).filter((item) => item.role === 'Site');
  const reachable = sites.filter((item) => item.health !== 'down');
  const activeUsers = (users ?? []).filter((user) => user.isActive);
  const admins = activeUsers.filter((user) => user.role === UserRole.ADMIN);
  const weekAgo = dayjs().subtract(7, 'day');
  const activeThisWeek = activeUsers.filter(
    (user) => user.lastActiveAt && dayjs(user.lastActiveAt).isAfter(weekAgo),
  ).length;
  const ingested = (signal?: UsageSignal) =>
    sites.reduce(
      (sum, item) =>
        sum +
        (item.usage?.hours ?? [])
          .filter((hour) => !signal || hour.signal === signal)
          .reduce((total, hour) => total + hour.rawBytes, 0),
      0,
    );
  const ingestedTotal = ingested();
  const stored = sites.reduce(
    (sum, item) => sum + (item.usage?.storage ?? []).reduce((total, s) => total + s.bytes, 0),
    0,
  );
  const behind = sites.filter(
    (item) => item.version && /^\d/.test(APP_VERSION) && item.version !== APP_VERSION,
  );

  const attention = useMemo((): Attention[] => {
    const items: Attention[] = [];
    for (const item of sites) {
      if (item.health === 'down') {
        items.push({
          text: `Site ${item.name} isn't answering${item.message ? `: ${item.message}` : ''}`,
          tab: 'sites',
          siteId: item.siteId,
        });
      }
    }
    for (const item of behind) {
      items.push({
        text: `Site ${item.name} runs ${item.version}; this control plane runs ${APP_VERSION}`,
        tab: 'updates',
      });
    }
    for (const item of sync?.sites ?? []) {
      if (item.pendingKeyFingerprint) {
        items.push({
          text: `Site ${item.siteId} serves a new key; accept it before it gets notification settings again`,
          tab: 'notifications',
        });
      }
      const stale =
        !item.fetchedAt || dayjs().diff(dayjs(item.fetchedAt), 'millisecond') > SYNC_STALE_MS;
      if (!item.current && stale) {
        items.push({
          text: `Notification settings on ${item.siteId} ${item.fetchedAt ? `last synced ${dayjs(item.fetchedAt).fromNow()}` : 'never synced'}`,
          tab: 'notifications',
        });
      }
    }
    const adminsWithoutMFA = admins.filter((user) => !user.mfaEnabled).length;
    if (adminsWithoutMFA > 0) {
      items.push({
        text: `${adminsWithoutMFA} admin${adminsWithoutMFA === 1 ? " doesn't" : "s don't"} have MFA set up yet`,
        tab: 'identity',
      });
    }
    for (const item of sites) {
      const disk =
        item.dashboard && percent(item.dashboard.diskUsedBytes, item.dashboard.diskTotalBytes);
      if (disk !== undefined && disk >= 85) {
        items.push({ text: `Disk on ${item.name} is ${Math.round(disk)}% full` });
      }
    }
    return items;
  }, [deployments, sync, users]);

  const ingestChart = useCallback(
    (colors: ChartColors): EChartsOption => {
      const start = dayjs().startOf('hour').subtract(23, 'hour');
      const hours = Array.from({ length: 24 }, (_, index) => start.add(index, 'hour'));
      return {
        animation: false,
        grid: { left: 52, right: 12, top: 28, bottom: 28 },
        legend: {
          top: 0,
          right: 0,
          type: 'scroll',
          textStyle: { color: colors.text, fontSize: 11 },
        },
        tooltip: {
          trigger: 'axis',
          backgroundColor: colors.tooltip,
          borderColor: colors.axis,
          textStyle: { color: colors.text, fontSize: 12 },
          valueFormatter: (value) => `${Number(value).toFixed(2)} GB`,
        },
        xAxis: {
          type: 'category',
          data: hours.map((hour) => hour.format('HH:00')),
          axisLabel: { color: colors.text, fontSize: 11, hideOverlap: true },
          axisLine: { lineStyle: { color: colors.axis } },
        },
        yAxis: {
          type: 'value',
          axisLabel: {
            color: colors.text,
            fontSize: 11,
            formatter: (value: number) => `${value} GB`,
          },
          splitLine: { lineStyle: { color: colors.split } },
        },
        series: sites
          .filter((item) => item.usage)
          .map((item, index) => ({
            name: item.name,
            type: 'bar',
            stack: 'ingest',
            barMaxWidth: 18,
            itemStyle: { color: SITE_COLORS[index % SITE_COLORS.length] },
            data: hours.map((hour) =>
              Number(
                (
                  (item.usage?.hours ?? [])
                    .filter((row) => dayjs(row.hour).isSame(hour, 'hour'))
                    .reduce((sum, row) => sum + row.rawBytes, 0) /
                  1024 ** 3
                ).toFixed(3),
              ),
            ),
          })),
      };
    },
    [deployments],
  );

  const storageChart = useCallback(
    (colors: ChartColors): EChartsOption => {
      const withUsage = sites.filter((item) => item.usage);
      return {
        animation: false,
        grid: { left: 110, right: 16, top: 28, bottom: 24 },
        legend: { top: 0, right: 0, textStyle: { color: colors.text, fontSize: 11 } },
        tooltip: {
          trigger: 'axis',
          axisPointer: { type: 'shadow' },
          backgroundColor: colors.tooltip,
          borderColor: colors.axis,
          textStyle: { color: colors.text, fontSize: 12 },
          valueFormatter: (value) => formatBytes(Number(value) * 1024 ** 3),
        },
        xAxis: {
          type: 'value',
          axisLabel: {
            color: colors.text,
            fontSize: 11,
            formatter: (value: number) => `${value} GB`,
          },
          splitLine: { lineStyle: { color: colors.split } },
        },
        yAxis: {
          type: 'category',
          data: withUsage.map((item) => item.name),
          axisLabel: { color: colors.text, fontSize: 12 },
          axisLine: { show: false },
          axisTick: { show: false },
        },
        series: SIGNALS.map((signal) => ({
          name: signal,
          type: 'bar',
          stack: 'stored',
          barMaxWidth: 16,
          itemStyle: { color: SIGNAL_COLOR[signal] },
          data: withUsage.map(
            (item) =>
              (item.usage?.storage.find((row) => row.signal === signal)?.bytes ?? 0) / 1024 ** 3,
          ),
        })),
      };
    },
    [deployments],
  );

  if (!deployments) {
    return (
      <div className="flex justify-center py-16">
        <Spin indicator={<LoadingOutlined spin />} />
      </div>
    );
  }

  const host = getApplicationServer() || window.location.origin;
  const healthURLs = [
    { label: 'Liveness', url: `${host}/api/v1/system/live` },
    { label: 'Readiness', url: `${host}/api/v1/system/ready` },
    { label: 'Uptime (database and disk)', url: `${host}/api/v1/downdetect/is-available` },
  ];
  const tiles = [
    ...(IS_CONTROL_PLANE
      ? [
          {
            label: 'Sites',
            value: `${reachable.length} / ${sites.length}`,
            note:
              sites.length - reachable.length > 0
                ? `${sites
                    .filter((item) => item.health === 'down')
                    .map((item) => item.name)
                    .join(', ')} unreachable`
                : 'all reachable',
          },
        ]
      : []),
    {
      label: 'Users',
      value: users ? String(activeUsers.length) : '—',
      note: `${admins.length} admin${admins.length === 1 ? '' : 's'} · ${activeThisWeek} active this week`,
    },
    {
      label: 'Ingested, 24 h',
      value: formatBytes(ingestedTotal),
      note:
        ingestedTotal > 0
          ? SIGNALS.map(
              (signal) => `${signal} ${Math.round((ingested(signal) / ingestedTotal) * 100)}%`,
            ).join(' · ')
          : 'nothing yet',
    },
    { label: 'Stored', value: formatBytes(stored), note: 'compressed' },
    {
      label: 'Version',
      value: displayVersion,
      note:
        behind.length > 0
          ? `${behind.length} Site${behind.length === 1 ? '' : 's'} on another version`
          : IS_CONTROL_PLANE
            ? 'every Site matches'
            : 'this deployment',
    },
  ];

  return (
    <div className="flex flex-col gap-4">
      <div className="grid grid-cols-2 gap-3 md:grid-cols-3 xl:grid-cols-5">
        {tiles.map((tile) => (
          <div
            key={tile.label}
            className="min-w-0 rounded-lg border border-gray-200 p-3 dark:border-gray-700"
          >
            <div className="text-xs font-medium tracking-wide text-gray-500 uppercase dark:text-gray-400">
              {tile.label}
            </div>
            <div className="mt-1 text-2xl font-semibold text-gray-900 dark:text-gray-100">
              {tile.value}
            </div>
            <Tooltip title={tile.note} placement="top">
              <div className="truncate text-xs text-gray-500 dark:text-gray-400">{tile.note}</div>
            </Tooltip>
          </div>
        ))}
      </div>

      <Panel
        title="System dashboard"
        subtitle={
          IS_CONTROL_PLANE
            ? 'Every deployment this control plane reaches, now'
            : 'This deployment, now'
        }
      >
        <Table<Deployment>
          rowKey="scope"
          size="small"
          pagination={false}
          dataSource={deployments}
          scroll={{ x: 900 }}
          columns={[
            {
              title: 'Status',
              key: 'health',
              width: 90,
              render: (_, item) => (
                <Tooltip title={item.message ?? item.health}>
                  <span className="inline-flex items-center gap-1.5 text-xs">
                    <span
                      className="inline-block h-2.5 w-2.5 rounded-full"
                      style={{ background: HEALTH_COLOR[item.health] }}
                    />
                    {item.health === 'down' ? 'Down' : item.health === 'degraded' ? 'Slow' : 'Up'}
                  </span>
                </Tooltip>
              ),
            },
            {
              title: 'Deployment',
              key: 'name',
              render: (_, item) => <span className="font-medium">{item.name}</span>,
            },
            { title: 'Role', dataIndex: 'role', width: 120 },
            {
              title: 'Version',
              key: 'version',
              width: 110,
              render: (_, item) => <span className="font-mono text-xs">{item.version ?? '—'}</span>,
            },
            {
              title: 'CPU',
              key: 'cpu',
              width: 120,
              render: (_, item) => meter(item.dashboard?.cpuPercent, 'CPU'),
            },
            {
              title: 'Memory',
              key: 'memory',
              width: 120,
              render: (_, item) =>
                item.dashboard
                  ? meter(
                      percent(item.dashboard.memoryUsedBytes, item.dashboard.memoryTotalBytes),
                      `${formatBytes(item.dashboard.memoryUsedBytes)} of ${formatBytes(item.dashboard.memoryTotalBytes)}`,
                    )
                  : '—',
            },
            {
              title: 'Disk',
              key: 'disk',
              width: 120,
              render: (_, item) =>
                item.dashboard
                  ? meter(
                      percent(item.dashboard.diskUsedBytes, item.dashboard.diskTotalBytes),
                      `${formatBytes(item.dashboard.diskUsedBytes)} of ${formatBytes(item.dashboard.diskTotalBytes)}`,
                    )
                  : '—',
            },
            {
              title: 'Logs in',
              key: 'ingest',
              width: 130,
              render: (_, item) =>
                item.dashboard
                  ? `${formatBytes(item.dashboard.ingestedRawBytes)} / ${item.dashboard.ingestionWindowMinutes} min`
                  : '—',
            },
            {
              title: 'Data from',
              key: 'oldest',
              width: 120,
              render: (_, item) => {
                const oldest = (item.usage?.storage ?? [])
                  .map((row) => row.oldest)
                  .filter((value): value is string => !!value)
                  .sort()[0];
                return oldest ? dayjs(oldest).format('D MMM YYYY') : '—';
              },
            },
          ]}
        />
      </Panel>

      <div className="grid gap-4 lg:grid-cols-2">
        <Panel title="Needs attention" subtitle="Click to go to the right tab">
          {attention.length === 0 ? (
            <div className="py-3 text-sm text-gray-500 dark:text-gray-400">
              Nothing needs attention.
            </div>
          ) : (
            <ul className="m-0 flex list-none flex-col divide-y divide-gray-100 p-0 dark:divide-gray-700">
              {attention.map((item) => (
                <li
                  key={item.text}
                  className="flex items-center justify-between gap-3 py-2 text-sm"
                >
                  <span className="text-gray-800 dark:text-gray-100">{item.text}</span>
                  {item.tab && (
                    <Button
                      size="small"
                      type="link"
                      className="shrink-0"
                      onClick={() => onNavigate(item.tab!, item.siteId)}
                    >
                      {TAB_LABEL[item.tab]} <RightOutlined />
                    </Button>
                  )}
                </li>
              ))}
            </ul>
          )}
        </Panel>
        <Panel
          title="Recent activity"
          subtitle="From the audit log"
          extra={
            <Button size="small" type="link" onClick={() => onNavigate('audit')}>
              All audit logs <RightOutlined />
            </Button>
          }
        >
          {!activity ? (
            <Spin indicator={<LoadingOutlined spin />} />
          ) : activity.length === 0 ? (
            <div className="py-3 text-sm text-gray-500 dark:text-gray-400">No activity yet.</div>
          ) : (
            <ul className="m-0 flex list-none flex-col divide-y divide-gray-100 p-0 dark:divide-gray-700">
              {activity.map((log) => (
                <li key={log.id} className="flex items-baseline justify-between gap-3 py-2 text-sm">
                  <span className="min-w-0 text-gray-800 dark:text-gray-100">
                    <b className="font-medium">{log.userName || log.userEmail || 'System'}</b>{' '}
                    {log.message}
                  </span>
                  <span className="shrink-0 text-xs text-gray-500 dark:text-gray-400">
                    {dayjs(log.createdAt).fromNow()}
                  </span>
                </li>
              ))}
            </ul>
          )}
        </Panel>
      </div>

      <div className="grid gap-4 lg:grid-cols-2">
        <Panel
          title={IS_CONTROL_PLANE ? 'Ingest by Site' : 'Ingest'}
          subtitle="GB received per hour, last 24 hours (uncompressed)"
        >
          <EChart option={ingestChart} height={240} />
        </Panel>
        <Panel
          title="Storage by signal"
          subtitle={IS_CONTROL_PLANE ? 'Compressed, per Site' : 'Compressed'}
        >
          <EChart option={storageChart} height={240} />
        </Panel>
      </div>

      <Panel title="Health checks" subtitle="For your load balancer and uptime monitoring">
        <div className="flex flex-col gap-2">
          {healthURLs.map(({ label, url }) => (
            <div
              key={label}
              className="grid items-center gap-2 text-sm sm:grid-cols-[180px_1fr_auto]"
            >
              <span className="text-gray-500 dark:text-gray-300">{label}</span>
              <code className="truncate rounded bg-gray-50 px-2 py-1 font-mono text-xs dark:bg-gray-900">
                {url}
              </code>
              <Button
                size="small"
                icon={<CopyOutlined />}
                onClick={async () => {
                  await copyToClipboard(url);
                  message.success(`${label} URL copied`);
                }}
              >
                Copy
              </Button>
            </div>
          ))}
        </div>
      </Panel>
    </div>
  );
};
