import { LoadingOutlined } from '@ant-design/icons';
import { Alert, App, Button, Drawer, Input, Segmented, Select, Spin, Switch, Tooltip } from 'antd';
import type { JSX, ReactNode } from 'react';
import { useEffect, useState } from 'react';

import {
  type WebAnalyticsIpMode,
  type WebAnalyticsSettings,
  type WebAnalyticsSettingsUpdate,
  type WebAnalyticsSource,
  webAnalyticsApi,
} from '../../../entity/webAnalytics';
import { formatBytes, formatCount } from '../lib/format';

interface Props {
  projectId: string;
  onClose: () => void;
  onSaved: () => void;
}

/** The mapping table's columns: label and the normalized fields it needs. */
const MAPPING_COLUMNS: { label: string; fields: string[]; hint: string }[] = [
  {
    label: 'Client IP',
    fields: ['client_address'],
    hint: 'add the client address to the log format',
  },
  { label: 'Request', fields: ['http_request_method', 'url_path'], hint: 'method and path' },
  { label: 'Status', fields: ['http_response_status_code'], hint: 'status code' },
  {
    label: 'Bytes',
    fields: ['http_response_body_size'],
    hint: 'add $body_bytes_sent (Nginx) or %B (Apache)',
  },
  {
    label: 'Referrer',
    fields: ['http_request_header_referer'],
    hint: 'not in this format (Apache: use "combined")',
  },
  {
    label: 'User agent',
    fields: ['user_agent_original'],
    hint: 'not in this format (Apache: use "combined")',
  },
  {
    label: 'Duration',
    fields: ['http_server_request_duration'],
    hint: 'add $request_time (Nginx) or %D (Apache)',
  },
];

const IP_NOTES: Record<WebAnalyticsIpMode, string> = {
  full: 'Full IPs are personal data under GDPR; keep them only with a reason and a short retention.',
  truncate:
    'Last octet (IPv4) or last 80 bits (IPv6) dropped before the hour is summarised. Countries still work. ' +
    'The default: truncation can’t be undone, a hash with a stored salt can be reversed by trying every IPv4 address.',
  hash: 'Salted hash per project. Unique visitors still work; you can’t see the address.',
};

const Row = ({ label, children }: { label: string; children: ReactNode }) => (
  <>
    <span className="pt-1 text-xs text-gray-500 dark:text-gray-400">{label}</span>
    <div className="flex min-w-0 flex-col gap-1">{children}</div>
  </>
);

interface SourceDraft {
  name: string;
  mergedInto: string;
}

/** Web analytics settings: field mapping, source names and merges, privacy and rollups. */
export const WebAnalyticsSettingsDrawer = ({ projectId, onClose, onSaved }: Props): JSX.Element => {
  const { message } = App.useApp();
  const [settings, setSettings] = useState<WebAnalyticsSettings | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [saving, setSaving] = useState(false);
  const [ipMode, setIpMode] = useState<WebAnalyticsIpMode>('truncate');
  const [retention, setRetention] = useState(0);
  const [excludeBots, setExcludeBots] = useState(true);
  const [extensions, setExtensions] = useState('');
  const [geoEnabled, setGeoEnabled] = useState(true);
  const [sourceDrafts, setSourceDrafts] = useState<Record<string, SourceDraft>>({});

  const load = (next: WebAnalyticsSettings) => {
    setSettings(next);
    setIpMode(next.ipMode);
    setRetention(next.retentionDays);
    setExcludeBots(next.excludeBots);
    setExtensions(next.staticExtensions);
    setGeoEnabled(next.geoEnabled);
    setSourceDrafts(
      Object.fromEntries(
        next.sources.map((s) => [
          s.key,
          { name: s.name === s.defaultName ? '' : s.name, mergedInto: s.mergedInto ?? '' },
        ]),
      ),
    );
  };

  useEffect(() => {
    let cancelled = false;
    webAnalyticsApi
      .settings(projectId)
      .then((next) => !cancelled && load(next))
      .catch(
        (e: unknown) =>
          !cancelled && setError(e instanceof Error ? e.message : 'Failed to load settings'),
      );
    return () => {
      cancelled = true;
    };
  }, [projectId]);

  const save = async () => {
    if (!settings) return;
    const update: WebAnalyticsSettingsUpdate = {};
    if (ipMode !== settings.ipMode) update.ipMode = ipMode;
    if (retention !== settings.retentionDays) update.retentionDays = retention;
    if (excludeBots !== settings.excludeBots) update.excludeBots = excludeBots;
    if (extensions !== settings.staticExtensions) update.staticExtensions = extensions;
    if (geoEnabled !== settings.geoEnabled) update.geoEnabled = geoEnabled;
    const sources = settings.sources
      .filter((s) => {
        const draft = sourceDrafts[s.key];
        const name = s.name === s.defaultName ? '' : s.name;
        return draft && (draft.name.trim() !== name || draft.mergedInto !== (s.mergedInto ?? ''));
      })
      .map((s) => ({
        key: s.key,
        name: sourceDrafts[s.key].name.trim(),
        mergedInto: sourceDrafts[s.key].mergedInto,
      }));
    if (sources.length) update.sources = sources;
    if (Object.keys(update).length === 0) {
      onClose();
      return;
    }
    setSaving(true);
    try {
      load(await webAnalyticsApi.updateSettings(projectId, update));
      message.success('Settings saved. New hours use them; older hours keep how they were built.');
      onSaved();
      onClose();
    } catch (e: unknown) {
      message.error(e instanceof Error ? e.message : 'Failed to save settings');
    } finally {
      setSaving(false);
    }
  };

  const canManage = settings?.canManage ?? false;
  const mergeTargets = (source: WebAnalyticsSource) =>
    (settings?.sources ?? [])
      .filter((s) => s.key !== source.key && !sourceDrafts[s.key]?.mergedInto)
      .map((s) => ({ value: s.key, label: s.name }));

  return (
    <Drawer
      open
      onClose={onClose}
      size={Math.min(720, window.innerWidth)}
      title="Web analytics settings"
      extra={
        <Tooltip title={canManage ? undefined : 'Only project admins can change these settings'}>
          <Button type="primary" loading={saving} disabled={!settings || !canManage} onClick={save}>
            Save
          </Button>
        </Tooltip>
      }
    >
      {error && <Alert type="error" showIcon title={error} />}
      {!settings && !error && (
        <div className="flex justify-center py-16">
          <Spin indicator={<LoadingOutlined spin />} />
        </div>
      )}
      {settings && (
        <div className="flex flex-col gap-5 text-sm text-gray-800 dark:text-gray-100">
          <section className="flex flex-col gap-2">
            <strong className="font-semibold">Sources and field mapping</strong>
            <span className="text-xs text-gray-500 dark:text-gray-400">
              Obzly reads these fields from each source, under the names each web server and
              collector config writes them or OpenTelemetry’s names, mapped when queried. Rename a
              source, or merge it into another to count them as one.
            </span>
            {settings.sources.length === 0 ? (
              <span className="text-xs text-gray-500 dark:text-gray-400">
                No access-log sources yet. They appear here once logs with a status and a method or
                path arrive.
              </span>
            ) : (
              <div className="-mx-1 overflow-x-auto px-1">
                <table className="w-full border-collapse text-[12.5px]">
                  <thead>
                    <tr className="text-left text-xs text-gray-500 dark:text-gray-400">
                      <th className="border-b border-gray-200 px-2 py-1.5 font-medium dark:border-gray-700">
                        Source
                      </th>
                      <th className="border-b border-gray-200 px-2 py-1.5 font-medium dark:border-gray-700">
                        Format
                      </th>
                      {MAPPING_COLUMNS.map((c) => (
                        <th
                          key={c.label}
                          className="border-b border-gray-200 px-2 py-1.5 font-medium whitespace-nowrap dark:border-gray-700"
                        >
                          {c.label}
                        </th>
                      ))}
                    </tr>
                  </thead>
                  <tbody>
                    {settings.sources.map((source) => {
                      const draft = sourceDrafts[source.key] ?? { name: '', mergedInto: '' };
                      return (
                        <tr key={source.key} className="align-top">
                          <td className="min-w-[200px] border-b border-gray-200 px-2 py-2 dark:border-gray-700">
                            <Input
                              size="small"
                              aria-label={`Name of ${source.defaultName}`}
                              placeholder={source.defaultName}
                              value={draft.name}
                              disabled={!canManage}
                              maxLength={120}
                              onChange={(e) =>
                                setSourceDrafts((d) => ({
                                  ...d,
                                  [source.key]: { ...draft, name: e.target.value },
                                }))
                              }
                              className="font-mono"
                            />
                            <Select
                              size="small"
                              className="mt-1 w-full"
                              aria-label={`Merge ${source.defaultName} into`}
                              value={draft.mergedInto}
                              disabled={!canManage || source.mergedFrom.length > 0}
                              onChange={(value: string) =>
                                setSourceDrafts((d) => ({
                                  ...d,
                                  [source.key]: { ...draft, mergedInto: value },
                                }))
                              }
                              options={[
                                { value: '', label: 'Not merged' },
                                ...mergeTargets(source).map((t) => ({
                                  ...t,
                                  label: `Merged into ${t.label}`,
                                })),
                              ]}
                            />
                          </td>
                          <td className="border-b border-gray-200 px-2 py-2 text-xs dark:border-gray-700">
                            {source.format || 'n/a'}
                          </td>
                          {MAPPING_COLUMNS.map((column) => {
                            const names = column.fields
                              .map((f) => source.fields[f])
                              .filter(Boolean);
                            const found = names.length === column.fields.length;
                            return (
                              <td
                                key={column.label}
                                title={found ? names.join(', ') : column.hint}
                                className={`border-b border-gray-200 px-2 py-2 dark:border-gray-700 ${
                                  found
                                    ? 'text-emerald-600 dark:text-emerald-400'
                                    : 'text-red-600 dark:text-red-400'
                                }`}
                              >
                                {found ? '✓' : '✗'}
                              </td>
                            );
                          })}
                        </tr>
                      );
                    })}
                  </tbody>
                </table>
              </div>
            )}
            <span className="text-xs text-gray-500 dark:text-gray-400">
              ✓ found (hover for the field name) · ✗ missing from this format. Apache’s “common”
              format has no referrer or browser: switch it to “combined”.
            </span>
            <details className="text-xs text-gray-500 dark:text-gray-400">
              <summary className="cursor-pointer">Field names read for each field</summary>
              <dl className="mt-2 grid grid-cols-[minmax(110px,30%)_1fr] gap-x-3 gap-y-1">
                {settings.schema.map((field) => [
                  <dt key={`${field.name}-dt`}>{field.label}</dt>,
                  <dd key={`${field.name}-dd`} className="m-0 font-mono break-all">
                    {field.aliases.join(', ')}
                  </dd>,
                ])}
              </dl>
            </details>
          </section>

          <section className="grid grid-cols-[150px_minmax(0,1fr)] items-start gap-x-3.5 gap-y-2.5 border-t border-gray-200 pt-3 dark:border-gray-700">
            <strong className="col-span-2 font-semibold">Privacy and data</strong>
            <Row label="Visitor IPs">
              <Segmented
                aria-label="Visitor IPs"
                value={ipMode}
                disabled={!canManage}
                onChange={(value) => setIpMode(value as WebAnalyticsIpMode)}
                options={[
                  { value: 'full', label: 'Store full' },
                  { value: 'truncate', label: 'Truncate' },
                  { value: 'hash', label: 'Hash' },
                ]}
                className="self-start"
              />
              <span className="text-xs text-gray-500 dark:text-gray-400">{IP_NOTES[ipMode]}</span>
            </Row>
            <Row label="Keep rollups">
              <Segmented
                aria-label="Keep rollups"
                value={retention}
                disabled={!canManage}
                onChange={(value) => setRetention(Number(value))}
                options={[
                  {
                    value: 0,
                    label:
                      settings.logRetentionDays > 0
                        ? `Like the logs (${settings.logRetentionDays} d)`
                        : 'Like the logs',
                  },
                  { value: 400, label: '400 days' },
                  { value: 90, label: '90 days' },
                  ...(![0, 400, 90].includes(settings.retentionDays)
                    ? [{ value: settings.retentionDays, label: `${settings.retentionDays} days` }]
                    : []),
                ]}
                className="self-start"
              />
            </Row>
            <Row label="Bots and crawlers">
              <span className="flex items-center gap-2">
                <Switch
                  checked={excludeBots}
                  disabled={!canManage}
                  onChange={setExcludeBots}
                  aria-label="Leave crawlers out"
                />
                <span className="text-xs">
                  Leave known crawlers out of visitors and top lists (they still count in Requests)
                </span>
              </span>
            </Row>
            <Row label="Static files">
              <Input
                size="small"
                aria-label="Static file extensions"
                value={extensions}
                disabled={!canManage}
                onChange={(e) => setExtensions(e.target.value)}
                className="font-mono"
              />
              <span className="text-xs text-gray-500 dark:text-gray-400">
                Extensions (space separated) counted as Static files rather than pages.
              </span>
            </Row>
            <Row label="Country lookup">
              <Segmented
                aria-label="Country lookup"
                value={geoEnabled ? 'dbip' : 'off'}
                disabled={!canManage}
                onChange={(value) => setGeoEnabled(value === 'dbip')}
                options={[
                  { value: 'dbip', label: 'DB-IP Lite' },
                  { value: 'off', label: 'Off' },
                ]}
                className="self-start"
              />
              <span className="text-xs text-gray-500 dark:text-gray-400">
                {geoEnabled ? (
                  <>
                    Ships with Obzly. CC BY 4.0: the report shows “
                    <a
                      href="https://db-ip.com"
                      target="_blank"
                      rel="noopener noreferrer"
                      className="text-accent-600 dark:text-accent-400"
                    >
                      IP geolocation by DB-IP
                    </a>
                    ”.{' '}
                    {settings.geo.available
                      ? `Database loaded${settings.geo.databaseType ? ` (${settings.geo.databaseType})` : ''}.`
                      : `No database on this server: ${settings.geo.error ?? 'not found'}. Countries stay empty.`}{' '}
                    Another GeoIP2-Country file (e.g. your own MaxMind GeoLite2) can be set with
                    WEB_ANALYTICS_GEOIP_DB.
                  </>
                ) : (
                  'No country lookups. The Countries panel is empty.'
                )}
              </span>
            </Row>
          </section>

          <section className="flex flex-col gap-1.5 border-t border-gray-200 pt-3 dark:border-gray-700">
            <strong className="font-semibold">Rollups</strong>
            <span className="text-xs">
              Built every hour by a worker, about {settings.buildDelayMinutes} minutes after the
              hour ends; newer hours are read live.{' '}
              {settings.build
                ? `Last build ${new Date(settings.build.builtAt).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })} ` +
                  `(the ${new Date(settings.build.hour).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })} hour), ` +
                  `${(settings.build.durationMs / 1000).toFixed(1)} s, ${formatCount(settings.build.rowsRead)} requests read, ` +
                  'within the worker’s query memory cap.'
                : 'No hour built yet: the worker starts within a few minutes and backfills the last 7 days.'}
            </span>
            <span className="text-xs text-gray-500 dark:text-gray-400">
              Rollups for this project: {formatCount(settings.storage.hours)} hours, about{' '}
              {formatBytes(settings.storage.bytes)}. Changing Visitor IPs, Bots or Static files
              applies to new hours; older hours keep how they were built.
            </span>
          </section>
        </div>
      )}
    </Drawer>
  );
};
