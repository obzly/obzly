import { DownloadOutlined, LoadingOutlined, SettingOutlined } from '@ant-design/icons';
import { Alert, Button, Dropdown, Segmented, Select, Spin, Switch, Tooltip } from 'antd';
import dayjs from 'dayjs';
import type { JSX } from 'react';
import { useCallback, useEffect, useMemo, useRef, useState } from 'react';

import {
  type WebAnalyticsReport,
  type WebAnalyticsRow,
  type WebAnalyticsSource,
  webAnalyticsApi,
} from '../../../entity/webAnalytics';
import { downloadText, toCSV, useVisibleInterval } from '../../../shared/lib';
import { absoluteChange, relativeChange } from '../../../shared/lib/periodChange';
import { useTheme } from '../../../shared/theme';
import { Card, KpiTile } from '../../../shared/ui/v2';
import type { TimeRange } from '../../query/ui/TimeRangePickerComponent';
import {
  RANGE_PRESETS,
  type RangePreset,
  clockTime,
  countryName,
  formatBytes,
  formatCount,
  formatMs,
  formatShare,
  rangeStart,
} from '../lib/format';
import { type LogsTarget, logsQueryFor } from '../lib/logsQuery';
import { SourcePicker } from './SourcePicker';
import { WebAnalyticsSettingsDrawer } from './WebAnalyticsSettingsDrawer';
import { HeatmapPanel, RequestsChart, StatusPanel } from './charts';
import { ListPanel, type PanelColumn, type TagTone } from './panels';

interface Props {
  projectId: string;
  /** Back to Search on these clauses over range (the Logs views contract). */
  onOpenLogs: (clauses: string[], range: TimeRange) => void;
}

const LIVE_REFRESH_MS = 30_000;
const DB_IP_URL = 'https://db-ip.com';

// A project can have dozens of sources; listing them all buries the message.
const names = (sources: WebAnalyticsSource[]) =>
  sources.length <= 2 ? sources.map((s) => s.name).join(' and ') : `${sources.length} sources`;

const hitsColumn: PanelColumn = {
  label: 'Hits',
  value: (r) => formatCount(r.hits),
  csv: (r) => r.hits,
};
const visitorsColumn: PanelColumn = {
  label: 'Visitors',
  value: (r) => (r.visitors === undefined ? 'n/a' : formatCount(r.visitors)),
  csv: (r) => r.visitors ?? '',
};
const bandwidthColumn: PanelColumn = {
  label: 'Bandwidth',
  value: (r) => formatBytes(r.bytes),
  csv: (r) => r.bytes,
};

const shareColumn = (
  rows: WebAnalyticsRow[],
  of: (r: WebAnalyticsRow) => number = (r) => r.hits,
): PanelColumn => {
  const total = rows.reduce((sum, r) => sum + of(r), 0);
  return {
    label: 'Share',
    value: (r) => (total ? formatShare(of(r) / total) : 'n/a'),
    csv: (r) => (total ? (of(r) / total).toFixed(4) : ''),
  };
};

const ago = (iso: string | undefined, now: number): string => {
  if (!iso) return 'n/a';
  const minutes = Math.max(0, Math.round((now - new Date(iso).getTime()) / 60_000));
  if (minutes < 60) return `${minutes} min ago`;
  const hours = Math.round(minutes / 60);
  return hours < 48 ? `${hours} h ago` : `${Math.round(hours / 24)} d ago`;
};

/** Web analytics: GoAccess-style reports from the access logs the project's web servers send. */
export const WebAnalyticsComponent = ({ projectId, onOpenLogs }: Props): JSX.Element => {
  const { chartColors, status, definition } = useTheme();
  const [range, setRange] = useState<RangePreset>('24h');
  const [sources, setSources] = useState<string[]>([]);
  const [host, setHost] = useState('');
  const [live, setLive] = useState(true);
  const [report, setReport] = useState<WebAnalyticsReport | null>(null);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [showHow, setShowHow] = useState(false);
  const [settingsOpen, setSettingsOpen] = useState(false);
  const [refreshToken, setRefreshToken] = useState(0);
  const [loadedAt, setLoadedAt] = useState(() => Date.now());
  const abortRef = useRef<AbortController | null>(null);

  const load = useCallback(() => {
    abortRef.current?.abort();
    const controller = new AbortController();
    abortRef.current = controller;
    setLoading(true);
    const now = new Date();
    webAnalyticsApi
      .report(
        projectId,
        { from: rangeStart(range, now).toISOString(), sources, host: host || undefined },
        controller.signal,
      )
      .then((next) => {
        setReport(next);
        setError(null);
        setLoadedAt(Date.now());
      })
      .catch((e: unknown) => {
        if (controller.signal.aborted) return;
        setError(e instanceof Error ? e.message : 'Failed to build the report');
      })
      .finally(() => {
        if (!controller.signal.aborted) setLoading(false);
      });
  }, [projectId, range, sources, host]);

  useEffect(() => {
    load();
    return () => abortRef.current?.abort();
  }, [load, refreshToken]);

  useEffect(() => {
    setSources([]);
    setHost('');
  }, [projectId]);

  useVisibleInterval(() => setRefreshToken((t) => t + 1), LIVE_REFRESH_MS, live);

  const rangeInfo = RANGE_PRESETS.find((p) => p.value === range) ?? RANGE_PRESETS[2];
  const allSources = report?.sources ?? [];
  const selectedSources = useMemo(
    () => (report ? allSources.filter((s) => report.selected.includes(s.key)) : []),
    [report, allSources],
  );
  const allSelected = sources.length === 0;
  const openLogs = (target: LogsTarget) =>
    onOpenLogs([logsQueryFor(target, selectedSources, allSelected)], {
      from: dayjs(rangeStart(range, new Date(loadedAt))),
      to: dayjs(loadedAt),
    });

  const color = (i: number) => chartColors[i % chartColors.length];
  const trend = (values: number[]) =>
    values.length > 1 && values.some((v) => v > 0) ? values : undefined;

  const exportCsv = () => {
    if (!report) return;
    const sections: [string, WebAnalyticsRow[]][] = [
      ['Requested URLs', report.panels.pages],
      ['Not found (404)', report.panels.notFound],
      ['Status codes', report.panels.status],
      ['Referring sites', report.panels.referrers],
      ['Visitors by IP', report.panels.ips],
      ['Browsers', report.panels.browsers],
      ['Operating systems', report.panels.systems],
      ['Devices', report.panels.devices],
      ['Countries', report.panels.countries.map((r) => ({ ...r, key: countryName(r.key) }))],
      ['Static files', report.panels.static],
      ['Virtual hosts', report.panels.vhosts],
      ['Methods', report.panels.methods],
      ['Protocols', report.panels.protocols],
      ['TLS', report.panels.tls],
    ];
    const rows: (string | number)[][] = [
      ['Panel', 'Key', 'Hits', 'Visitors', 'Bandwidth (bytes)', '5xx', 'Country'],
    ];
    for (const [panel, list] of sections) {
      for (const r of list)
        rows.push([panel, r.key, r.hits, r.visitors ?? '', r.bytes, r.errors5xx, r.country ?? '']);
    }
    downloadText(`web-analytics-${range}.csv`, toCSV(rows), 'text/csv');
  };

  if (!report) {
    return (
      <div className="space-y-4">
        {error ? (
          <Alert type="error" showIcon title="Couldn’t build the report" description={error} />
        ) : (
          <div className="flex justify-center py-24">
            <Spin indicator={<LoadingOutlined spin />} />
          </div>
        )}
      </div>
    );
  }

  const { totals, previous, panels } = report;
  const series = report.series;
  const noDuration = selectedSources.filter((s) => !report.totals.durationSources.includes(s.key));
  const durationSources = selectedSources.filter((s) =>
    report.totals.durationSources.includes(s.key),
  );
  const noReferrer = selectedSources.filter(
    (s) => s.requests > 0 && (!s.hasReferrer || !s.hasUserAgent),
  );
  const noTls = selectedSources.filter((s) => s.requests > 0 && !s.hasTls);
  const selectedRequests = selectedSources.reduce((sum, s) => sum + s.requests, 0);
  const failedShare = totals.requests ? (totals.failed / totals.requests) * 100 : null;
  const previousFailedShare = previous?.requests
    ? (previous.failed / previous.requests) * 100
    : null;
  const now = loadedAt;
  const short = !report.rollups.used;

  const rollupNote = short
    ? 'not used for ranges under 2 hours'
    : report.rollups.hours > 0 && report.rollups.from && report.rollups.to
      ? `${report.rollups.hours} closed hour${report.rollups.hours === 1 ? '' : 's'}, ` +
        `${clockTime(report.rollups.from)}–${clockTime(report.rollups.to)}` +
        (report.rollups.lastBuiltAt ? `, built ${clockTime(report.rollups.lastBuiltAt)}` : '')
      : 'no closed hours summarised yet';
  const liveNote =
    `${short ? 'the whole range' : `${clockTime(report.live.from)}–${clockTime(report.live.to)}`} read from raw logs ` +
    `(${formatCount(report.live.rows)} requests, ${(report.live.durationMs / 1000).toFixed(1)} s` +
    `${report.live.cached ? ', shared' : ''})` +
    (report.live.partial ? ', partial: some of it took too long to read and is left out' : '') +
    (live ? ', refreshing every 30 s' : '');

  const kpis = (
    <section
      aria-label="Totals"
      className="grid grid-cols-[repeat(auto-fit,minmax(220px,1fr))] gap-4"
    >
      <KpiTile
        label="Requests"
        value={formatCount(totals.requests)}
        change={relativeChange(totals.requests, previous?.requests, 'neither')}
        trend={trend(series.map((b) => b.requests))}
        color={color(0)}
        onClick={() => openLogs({ kind: 'all' })}
      />
      <KpiTile
        label="Unique visitors"
        value={totals.visitors === null ? 'n/a' : formatCount(totals.visitors)}
        change={relativeChange(totals.visitors, previous?.visitors, 'up')}
        trend={totals.visitors === null ? undefined : trend(series.map((b) => b.visitors))}
        color={color(1)}
        note={
          totals.visitors === null
            ? 'The selected sources log no client address.'
            : `Distinct IP + browser${report.excludeBots ? ', crawlers left out' : ''}.`
        }
      />
      <KpiTile
        label="Failed requests"
        value={failedShare === null ? 'n/a' : failedShare.toFixed(1)}
        unit={failedShare === null ? undefined : '%'}
        change={absoluteChange(
          failedShare,
          previousFailedShare,
          'down',
          (d) => `${d.toFixed(1)} pt`,
        )}
        trend={trend(
          series.map((b) => (b.requests ? ((b.status4xx + b.status5xx) / b.requests) * 100 : 0)),
        )}
        color={status.bad}
        onClick={() => openLogs({ kind: 'atLeast', field: 'status', value: 400 })}
      />
      <KpiTile
        label="Not found (404)"
        value={formatCount(totals.notFound)}
        change={relativeChange(totals.notFound, previous?.notFound, 'down')}
        color={status.warn}
        onClick={() => openLogs({ kind: 'equals', field: 'status', value: '404' })}
      />
      <KpiTile
        label="Bandwidth"
        value={formatBytes(totals.bytes)}
        change={relativeChange(totals.bytes, previous?.bytes, 'neither')}
        trend={trend(series.map((b) => b.bytes))}
        color={color(2)}
        note="Sum of response body bytes."
      />
      <KpiTile
        label="Response time p95"
        value={totals.p95Ms === null ? 'n/a' : formatMs(totals.p95Ms)}
        change={
          totals.p95Ms === null
            ? { text: 'not logged', tone: 'neutral' }
            : noDuration.length
              ? {
                  text: `${durationSources.length} of ${selectedSources.length} sources`,
                  tone: 'neutral',
                }
              : relativeChange(totals.p95Ms, previous?.p95Ms, 'down')
        }
        note={
          !noDuration.length
            ? `From ${names(durationSources)}.`
            : !durationSources.length
              ? 'No data to show. Add $request_time (Nginx) or %D (Apache) to the log format.'
              : `From ${names(durationSources)} only. ${names(noDuration)} ` +
                `${noDuration.length === 1 ? 'doesn’t' : 'don’t'} log response time.`
        }
      />
    </section>
  );

  const pathTag = (row: WebAnalyticsRow): { text: string; tone: TagTone } | undefined => {
    if (row.tag === 'scanner') return { text: 'scanner', tone: 'muted' };
    if (row.tag === 'broken link') return { text: 'broken link', tone: 'warn' };
    return undefined;
  };
  const geoOn = report.geo.enabled;
  const methodRows = [...panels.methods, ...panels.protocols];

  return (
    <div className="space-y-4">
      <div className="flex flex-wrap items-center gap-2.5">
        <div className="min-w-[280px] flex-1">
          <h2 className="m-0 text-[22px] font-semibold text-gray-900 dark:text-gray-50">
            Web analytics
          </h2>
          <p className="m-0 mt-0.5 text-sm text-gray-500 dark:text-gray-400">
            From the access logs your web servers send · {rangeInfo.text}
            {loading && <LoadingOutlined spin className="ml-2" />}
          </p>
        </div>
        <Segmented
          aria-label="Time range"
          value={range}
          onChange={(value) => setRange(value as RangePreset)}
          options={RANGE_PRESETS.map((p) => ({ value: p.value, label: p.label }))}
        />
        <SourcePicker
          sources={allSources}
          selected={sources}
          onApply={(keys) => {
            setSources(keys);
            setHost('');
          }}
        />
        <Select
          aria-label="Host"
          className="min-w-[180px]"
          value={host}
          onChange={setHost}
          popupMatchSelectWidth={false}
          options={[
            { value: '', label: 'All hosts' },
            ...report.hosts.map((h) => ({
              value: h.host,
              label: (
                <span className="flex justify-between gap-3">
                  <span>{h.host}</span>
                  <span className="text-xs text-gray-500 dark:text-gray-400">
                    {selectedRequests ? formatShare(h.requests / selectedRequests) : ''}
                  </span>
                </span>
              ),
            })),
          ]}
        />
        <Tooltip title="Update the current hour every 30 s">
          <span className="inline-flex items-center gap-2 rounded-[10px] border border-gray-200 px-3 py-1.5 text-sm text-gray-500 dark:border-gray-700 dark:text-gray-400">
            Live
            <Switch size="small" checked={live} onChange={setLive} aria-label="Live" />
          </span>
        </Tooltip>
        <Tooltip title="Web analytics settings (fields, privacy, bots, GeoIP)">
          <Button
            icon={<SettingOutlined />}
            aria-label="Web analytics settings"
            onClick={() => setSettingsOpen(true)}
          />
        </Tooltip>
        <Dropdown
          trigger={['click']}
          menu={{
            items: [
              { key: 'csv', label: 'CSV (every panel)', onClick: exportCsv },
              { key: 'pdf', label: 'Print or save as PDF', onClick: () => window.print() },
            ],
          }}
        >
          <Tooltip title="Export this report (CSV of every panel, or PDF)">
            <Button icon={<DownloadOutlined />} aria-label="Export report" />
          </Tooltip>
        </Dropdown>
      </div>

      {!allSelected && selectedSources.length > 0 && (
        <div className="flex flex-wrap items-center gap-1.5">
          <span className="text-xs text-gray-500 dark:text-gray-400">Report built from</span>
          {selectedSources.map((s) => (
            <span
              key={s.key}
              className="inline-flex items-center gap-1 rounded-lg border border-gray-200 bg-gray-50 px-2 py-0.5 font-mono text-[12.5px] dark:border-gray-700 dark:bg-gray-900/60"
            >
              {s.name}
              {selectedSources.length > 1 && (
                <button
                  type="button"
                  aria-label={`Remove ${s.name} from the report`}
                  className="cursor-pointer border-0 bg-transparent p-0 text-gray-500"
                  onClick={() => setSources(sources.filter((k) => k !== s.key))}
                >
                  ×
                </button>
              )}
            </span>
          ))}
        </div>
      )}

      <div className="flex flex-wrap items-center gap-2.5 rounded-xl border border-gray-200 bg-white px-3.5 py-2.5 text-[13px] text-gray-800 dark:border-gray-700 dark:bg-gray-800 dark:text-gray-100">
        <span className="bg-accent-500/10 text-accent-700 dark:text-accent-400 rounded-full px-2.5 py-0.5 text-xs font-medium">
          Rollups
        </span>
        <span>{rollupNote}</span>
        <span className="text-gray-400">+</span>
        <span
          className={`inline-flex items-center gap-1.5 rounded-full px-2.5 py-0.5 text-xs font-medium ${
            live
              ? 'bg-accent-500/10 text-accent-700 dark:text-accent-400'
              : 'bg-gray-100 text-gray-500 dark:bg-gray-700 dark:text-gray-300'
          }`}
        >
          <span className={`h-2 w-2 rounded-full ${live ? 'bg-accent-500' : 'bg-gray-400'}`} />
          {live ? 'Live' : 'Live paused'}
        </span>
        <span>{liveNote}</span>
        <span className="flex-1" />
        <Button type="link" size="small" className="!px-0" onClick={() => setShowHow((v) => !v)}>
          {showHow ? 'Hide' : 'How is this built?'}
        </Button>
      </div>

      {showHow && (
        <Card className="!gap-2 bg-gray-50 dark:bg-gray-900/60">
          <strong className="font-semibold text-gray-900 dark:text-gray-50">
            How this report is built
          </strong>
          <p className="m-0 mt-1.5 text-[13px] text-gray-700 dark:text-gray-200">
            Each closed hour is summarised once, a few minutes after it ends, into small rollup
            tables (counts by status, path, referrer, browser, country…) kept per source and host.
            The hour in progress, and any hour not summarised yet, is read from the raw logs when
            you open the page and every 30 s while Live is on. A range merges the stored hours
            (whole days past a few days) with the live part, so 7 or 30 days load as fast as one
            hour. Ranges under 2 hours are read from raw logs.
          </p>
          <p className="m-0 mt-1.5 text-[13px] text-gray-500 dark:text-gray-400">
            Unique visitors can’t be added across hours, so each hour keeps a small mergeable sketch
            (HyperLogLog) instead of a count: any range counts a visitor once, within about 2%. Top
            lists keep each hour’s busiest 100 rows. Rollups follow the project’s log retention
            unless set shorter in settings.
          </p>
        </Card>
      )}

      {error && (
        <Alert type="warning" showIcon title="Couldn’t refresh the report" description={error} />
      )}

      {report.rollups.missing > 0 && (
        <Alert
          type="info"
          showIcon
          title={`${report.rollups.missing} hour${report.rollups.missing === 1 ? '' : 's'} in this range ${
            report.rollups.missing === 1 ? 'isn’t' : 'aren’t'
          } summarised yet and ${report.rollups.missing === 1 ? 'is' : 'are'} left out.`}
          description="The worker builds missing hours of the last 7 days, newest first, a few at a time."
        />
      )}

      {noReferrer.length > 0 && (
        <div className="flex items-start gap-2.5 rounded-xl bg-orange-500/10 px-3.5 py-2.5 text-[13px] text-gray-800 dark:text-gray-100">
          <span className="font-bold text-orange-600 dark:text-orange-400">!</span>
          <span className="flex-1">
            <strong className="font-semibold">{names(noReferrer)}</strong>{' '}
            {noReferrer.length === 1 ? 'logs' : 'log'} no referrer or browser
            {noReferrer.length === 1 && noReferrer[0].format
              ? ` (${noReferrer[0].format} format)`
              : ''}
            . Referrers, browsers and operating systems leave out{' '}
            {selectedRequests
              ? formatShare(noReferrer.reduce((sum, s) => sum + s.requests, 0) / selectedRequests)
              : 'some'}{' '}
            of requests.{' '}
            <Button
              type="link"
              size="small"
              className="!h-auto !p-0"
              onClick={() => setSettingsOpen(true)}
            >
              Fix in field mapping
            </Button>
          </span>
        </div>
      )}

      {allSources.length === 0 ? (
        <Card>
          <strong className="font-semibold text-gray-900 dark:text-gray-50">
            No access logs yet
          </strong>
          <p className="m-0 mt-1.5 text-sm text-gray-500 dark:text-gray-400">
            Web analytics reads logs that carry access-log fields: a status code plus a method or
            path. Add a web server (Nginx, Apache, Envoy, Traefik, HAProxy, IIS) under Settings ›
            Data sources, or send logs with OpenTelemetry names such as http_response_status_code
            and url_path. Kubernetes ingress logs in the combined format are recognised as they are.
          </p>
        </Card>
      ) : (
        <>
          {kpis}

          <RequestsChart
            series={series}
            bucketSeconds={report.bucketSeconds}
            rangeText={rangeInfo.text}
            status={status}
            visitorsColor={color(1)}
            bandwidthColor={color(2)}
            textColor={definition.colors.text}
            visitorsKnown={totals.visitors !== null}
          />

          <div className="grid grid-cols-[repeat(auto-fit,minmax(380px,1fr))] gap-4">
            <ListPanel
              title="Requested URLs"
              subtitle="Pages and API calls, static files left out"
              keyLabel="Path"
              rows={panels.pages}
              columns={[
                hitsColumn,
                visitorsColumn,
                bandwidthColumn,
                {
                  label: '5xx',
                  value: (r) => formatShare(r.hits ? r.errors5xx / r.hits : 0),
                  csv: (r) => r.errors5xx,
                  tone: (r) =>
                    r.hits && r.errors5xx / r.hits >= 0.01
                      ? 'text-red-600 dark:text-red-400'
                      : undefined,
                },
              ]}
              color={color(0)}
              mono
              note="IDs in paths are grouped (/catalog/:id), the same way Obzly groups log patterns."
              onOpenRow={(r) => openLogs({ kind: 'equals', field: 'path', value: r.key })}
              onOpenAll={() => openLogs({ kind: 'all' })}
              csvName="requested-urls"
            />
            <ListPanel
              title="Not found (404)"
              subtitle="Most are scanners; real broken links are tagged"
              keyLabel="Path"
              rows={panels.notFound}
              columns={[
                hitsColumn,
                visitorsColumn,
                {
                  label: 'Last seen',
                  value: (r) => ago(r.lastSeen, now),
                  csv: (r) => r.lastSeen ?? '',
                },
              ]}
              color={status.warn}
              mono
              tag={pathTag}
              onOpenRow={(r) => openLogs({ kind: 'equals', field: 'path', value: r.key })}
              onOpenAll={() => openLogs({ kind: 'equals', field: 'status', value: '404' })}
              csvName="not-found"
            />
            <StatusPanel
              rows={panels.status}
              total={totals.requests}
              status={status}
              onOpenStatus={(code) => openLogs({ kind: 'equals', field: 'status', value: code })}
              onOpenAll={() => openLogs({ kind: 'all' })}
            />
            <ListPanel
              title="Referring sites"
              subtitle="Where visitors came from; own hosts left out"
              keyLabel="Site"
              rows={panels.referrers}
              empty={
                noReferrer.length === selectedSources.length && noReferrer.length > 0
                  ? 'No data to show. The selected sources don’t log the referrer.'
                  : undefined
              }
              columns={[hitsColumn, visitorsColumn]}
              color={color(3)}
              tag={(r) => (r.tag === 'search' ? { text: 'search', tone: 'info' } : undefined)}
              note={
                noReferrer.length === selectedSources.length && noReferrer.length > 0
                  ? undefined
                  : noReferrer.length
                    ? `Leaves out ${names(noReferrer)} (no referrer in the log format).`
                    : undefined
              }
              onOpenRow={(r) =>
                r.key === '(direct or none)'
                  ? undefined
                  : openLogs({ kind: 'contains', field: 'referrer', value: r.key })
              }
              onOpenAll={() => openLogs({ kind: 'all' })}
              csvName="referring-sites"
            />
            <ListPanel
              title="Visitors by IP"
              subtitle={
                report.ipMode === 'full'
                  ? 'Full addresses are stored'
                  : report.ipMode === 'truncate'
                    ? 'Addresses truncated to /24 (IPv4) and /48 (IPv6)'
                    : 'Addresses hashed per project; the same visitor keeps the same hash'
              }
              keyLabel="Address"
              rows={panels.ips}
              columns={[
                hitsColumn,
                bandwidthColumn,
                ...(geoOn
                  ? [
                      {
                        label: 'Country',
                        value: (r: WebAnalyticsRow) => (r.country ? countryName(r.country) : 'n/a'),
                        csv: (r: WebAnalyticsRow) => r.country ?? '',
                      },
                    ]
                  : []),
              ]}
              color={color(4)}
              mono
              note={
                report.excludeBots
                  ? 'Known crawlers are left out (settings).'
                  : 'Crawlers are included.'
              }
              onOpenRow={
                report.ipMode === 'hash'
                  ? undefined
                  : (r) =>
                      openLogs(
                        report.ipMode === 'full'
                          ? { kind: 'equals', field: 'client', value: r.key }
                          : {
                              kind: 'contains',
                              field: 'client',
                              value: r.key.replace(/(\.0\/24|::\/48)$/, ''),
                            },
                      )
              }
              csvName="visitors-by-ip"
            />
            <ListPanel
              title="Browsers"
              subtitle="From the User-Agent header"
              keyLabel="Browser"
              rows={panels.browsers}
              columns={[visitorsColumn, shareColumn(panels.browsers, (r) => r.visitors ?? r.hits)]}
              color={color(0)}
              tag={(r) => (r.key === 'Crawlers' ? { text: 'bot', tone: 'muted' } : undefined)}
              note={
                noReferrer.length
                  ? `Leaves out ${names(noReferrer)} (no User-Agent in the log format).`
                  : undefined
              }
              onOpenRow={(r) =>
                r.key === 'Other' || r.key === 'Crawlers'
                  ? undefined
                  : openLogs({ kind: 'contains', field: 'userAgent', value: r.key })
              }
              csvName="browsers"
            />
            <ListPanel
              title="Operating systems"
              subtitle="From the User-Agent header"
              keyLabel="System"
              rows={panels.systems}
              columns={[visitorsColumn, shareColumn(panels.systems, (r) => r.visitors ?? r.hits)]}
              color={color(3)}
              csvName="operating-systems"
            />
            <ListPanel
              title="Countries"
              subtitle={
                !geoOn
                  ? 'Country lookup is off'
                  : report.geo.available
                    ? 'From the visitor IP, looked up when the hour is summarised (DB-IP Lite)'
                    : 'No country database on this server'
              }
              keyLabel="Country"
              rows={panels.countries}
              label={(r) => countryName(r.key)}
              columns={[visitorsColumn, shareColumn(panels.countries, (r) => r.visitors ?? r.hits)]}
              color={color(4)}
              empty={
                !geoOn
                  ? 'Turn on country lookup in settings to see countries.'
                  : !report.geo.available
                    ? 'The country database is missing (see WEB_ANALYTICS_GEOIP_DB), so countries are unknown.'
                    : undefined
              }
              note={
                geoOn ? (
                  <a
                    href={DB_IP_URL}
                    target="_blank"
                    rel="noopener noreferrer"
                    className="text-accent-600 dark:text-accent-400"
                  >
                    {report.geo.attribution}
                  </a>
                ) : undefined
              }
              csvName="countries"
            />
            <HeatmapPanel
              hours={report.heatmap}
              accent={definition.colors.accent}
              now={new Date(loadedAt)}
            />
            <ListPanel
              title="Static files"
              subtitle="By the extensions in settings"
              keyLabel="File"
              rows={panels.static}
              columns={[hitsColumn, bandwidthColumn]}
              color={color(1)}
              mono
              onOpenRow={(r) => openLogs({ kind: 'equals', field: 'path', value: r.key })}
              csvName="static-files"
            />
            <ListPanel
              title="Virtual hosts"
              subtitle="From the Host header"
              keyLabel="Host"
              rows={panels.vhosts}
              columns={[
                hitsColumn,
                bandwidthColumn,
                {
                  label: '5xx',
                  value: (r) => formatShare(r.hits ? r.errors5xx / r.hits : 0),
                  csv: (r) => r.errors5xx,
                  tone: (r) =>
                    r.hits && r.errors5xx / r.hits >= 0.01
                      ? 'text-red-600 dark:text-red-400'
                      : undefined,
                },
              ]}
              color={color(2)}
              mono
              empty="No data to show. The selected sources don’t log the Host header."
              onOpenRow={(r) => openLogs({ kind: 'equals', field: 'host', value: r.key })}
              csvName="virtual-hosts"
            />
            <ListPanel
              title="Methods and protocols"
              subtitle="Request method, then HTTP version"
              keyLabel="Method"
              rows={methodRows}
              columns={[
                hitsColumn,
                {
                  label: 'Share',
                  value: (r) => (totals.requests ? formatShare(r.hits / totals.requests) : 'n/a'),
                  csv: (r) => (totals.requests ? (r.hits / totals.requests).toFixed(4) : ''),
                },
              ]}
              color={color(1)}
              mono
              onOpenRow={(r) =>
                openLogs(
                  r.key.startsWith('HTTP/')
                    ? { kind: 'contains', field: 'protocol', value: r.key.replace('HTTP/', '') }
                    : { kind: 'equals', field: 'method', value: r.key },
                )
              }
              csvName="methods-and-protocols"
            />
            <ListPanel
              title="TLS"
              subtitle={
                selectedSources.length > noTls.length
                  ? `Protocol versions from ${names(selectedSources.filter((s) => s.hasTls))}`
                  : 'Protocol versions'
              }
              keyLabel="Version"
              rows={panels.tls}
              columns={[hitsColumn, shareColumn(panels.tls)]}
              color={color(3)}
              mono
              empty={
                noTls.length
                  ? 'No data to show. Add $ssl_protocol (Nginx) or %{SSL_PROTOCOL}x (Apache) to the log format.'
                  : undefined
              }
              note={
                panels.tls.length && noTls.length
                  ? `${names(noTls)} ${noTls.length === 1 ? 'doesn’t' : 'don’t'} log TLS, so ${
                      noTls.length === 1 ? 'its' : 'their'
                    } requests aren’t counted here.`
                  : undefined
              }
              onOpenRow={(r) =>
                openLogs({ kind: 'contains', field: 'tls', value: r.key.replace('TLSv', '') })
              }
              csvName="tls"
            />
            <ListPanel
              title="Devices"
              subtitle="Desktop, mobile, tablet, from the User-Agent header"
              keyLabel="Device"
              rows={panels.devices}
              columns={[visitorsColumn, shareColumn(panels.devices, (r) => r.visitors ?? r.hits)]}
              color={color(5)}
              csvName="devices"
            />
          </div>
        </>
      )}

      <p className="m-0 text-xs text-gray-500 dark:text-gray-400">
        {geoOn && (
          <>
            <a
              href={DB_IP_URL}
              target="_blank"
              rel="noopener noreferrer"
              className="text-accent-600 dark:text-accent-400"
            >
              IP geolocation by DB-IP
            </a>{' '}
            (CC BY 4.0).{' '}
          </>
        )}
        Visitors are distinct client address + browser; bots
        {report.excludeBots ? ' are left out' : ' are included'}.
      </p>

      {settingsOpen && (
        <WebAnalyticsSettingsDrawer
          projectId={projectId}
          onClose={() => setSettingsOpen(false)}
          onSaved={() => setRefreshToken((t) => t + 1)}
        />
      )}
    </div>
  );
};
