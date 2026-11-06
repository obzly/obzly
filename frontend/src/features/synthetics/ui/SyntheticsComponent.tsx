// The control plane's Synthetics tab (docs/SYNTHETICS.md): monitors run by
// obzly-probe agents, their results read from each monitor's destination
// Site project.
import { DownloadOutlined, LoadingOutlined, PlusOutlined, UploadOutlined } from '@ant-design/icons';
import { Alert, App, Button, Segmented, Spin, Tabs } from 'antd';
import { useCallback, useEffect, useMemo, useRef, useState } from 'react';

import { type SettingsChannel, notificationSettingsApi } from '../../../entity/alerting';
import {
  type CheckView,
  type Destination,
  type MaintenanceOccurrence,
  type MaintenanceWindow,
  type Probe,
  maintenanceApi,
  syntheticsApi,
} from '../../../entity/synthetics';
import { downloadText, useVisibleInterval } from '../../../shared/lib';
import { occurrencesKey } from '../lib/maintenance';
import { RANGE_SECONDS, type RangeKey, useMonitorData } from '../lib/useMonitorData';
import { DependencyMapComponent } from './DependencyMapComponent';
import { DestinationsComponent } from './DestinationsComponent';
import { ImportDrawer } from './ImportDrawer';
import { MaintenanceComponent } from './MaintenanceComponent';
import { MonitorDetailDrawer } from './MonitorDetailDrawer';
import { MonitorEditorComponent } from './MonitorEditorComponent';
import { MonitorsTableComponent } from './MonitorsTableComponent';
import { OverviewComponent } from './OverviewComponent';
import { ProbesComponent } from './ProbesComponent';

interface Props {
  contentHeight: number;
  /** Admins manage monitors, probes and destinations; everyone else reads. */
  canManage: boolean;
}

type View = 'overview' | 'monitors' | 'maintenance' | 'map' | 'probes' | 'destinations';

const REFRESH_MS = 60_000;
// The views that show monitor results; the rest (and the editor) don't poll.
const POLLED_VIEWS = new Set<View>(['overview', 'monitors', 'map']);
// Occurrences loaded: the longest range back, and the calendar's 14 days ahead plus a day's margin.
const MAINTENANCE_BACK_MS = RANGE_SECONDS['7d'] * 1000;
const MAINTENANCE_AHEAD_MS = 15 * 86_400_000;

export const SyntheticsComponent = ({ contentHeight, canManage }: Props) => {
  const { message } = App.useApp();
  const [checks, setChecks] = useState<CheckView[]>();
  const [probes, setProbes] = useState<Probe[]>([]);
  const [destinations, setDestinations] = useState<Destination[]>([]);
  const [channels, setChannels] = useState<SettingsChannel[]>([]);
  const [loadError, setLoadError] = useState<string>();
  const [view, setView] = useState<View>('overview');
  const [range, setRange] = useState<RangeKey>('24h');
  const [refresh, setRefresh] = useState(0);
  const [openId, setOpenId] = useState<string>();
  // The editor has no tab or link of its own: it opens only from Edit or
  // New monitor, so a saved monitor is never overwritten by accident.
  const [editing, setEditing] = useState<CheckView | 'new'>();
  const [importing, setImporting] = useState(false);
  const [busy, setBusy] = useState<Set<string>>(new Set());
  const [windows, setWindows] = useState<MaintenanceWindow[]>([]);
  const [occurrences, setOccurrences] = useState<MaintenanceOccurrence[]>([]);
  const [editWindowId, setEditWindowId] = useState<string>();
  // Reloaded maintenance is a new array each time: keep the old one when
  // nothing changed, so monitor queries don't restart.
  const windowsKey = useRef<string>(undefined);
  const occurrencesKeyRef = useRef<string>(undefined);

  // A new checks array restarts every monitor query, so keep the old one
  // when nothing changed. Resolves to whether the list changed.
  const checksKey = useRef<string>(undefined);
  const loadChecks = useCallback(async (): Promise<boolean> => {
    try {
      const next = await syntheticsApi.listChecks();
      setLoadError(undefined);
      const key = JSON.stringify(next);
      if (key === checksKey.current) return false;
      checksKey.current = key;
      setChecks(next);
      return true;
    } catch (reason) {
      setLoadError((reason as Error).message);
      return false;
    }
  }, []);
  const loadAdmin = useCallback(async () => {
    if (!canManage) return;
    try {
      const [nextProbes, nextDestinations, nextChannels] = await Promise.all([
        syntheticsApi.listProbes(),
        syntheticsApi.listDestinations(),
        // Monitor alerts use the central channels only (synced to every Site).
        notificationSettingsApi
          .listChannels()
          .then((list) => list.filter((channel) => channel.source === 'central'))
          .catch(() => [] as SettingsChannel[]),
      ]);
      setProbes(nextProbes);
      setDestinations(nextDestinations);
      setChannels(nextChannels);
    } catch (reason) {
      message.error((reason as Error).message);
    }
  }, [canManage, message]);

  const loadMaintenance = useCallback(async () => {
    try {
      const now = Date.now();
      const [nextWindows, nextOccurrences] = await Promise.all([
        maintenanceApi.list(),
        maintenanceApi.occurrences(
          new Date(now - MAINTENANCE_BACK_MS),
          new Date(now + MAINTENANCE_AHEAD_MS),
        ),
      ]);
      const nextWindowsKey = JSON.stringify(nextWindows);
      if (nextWindowsKey !== windowsKey.current) {
        windowsKey.current = nextWindowsKey;
        setWindows(nextWindows);
      }
      const nextOccurrencesKey = occurrencesKey(nextOccurrences);
      if (nextOccurrencesKey !== occurrencesKeyRef.current) {
        occurrencesKeyRef.current = nextOccurrencesKey;
        setOccurrences(nextOccurrences);
      }
    } catch (reason) {
      message.error(`Couldn't load maintenance windows: ${(reason as Error).message}`);
    }
  }, [message]);

  const lastTick = useRef(0);
  useEffect(() => {
    lastTick.current = Date.now();
    void loadChecks();
    void loadAdmin();
    void loadMaintenance();
  }, [loadChecks, loadAdmin, loadMaintenance]);
  // One monitor reload per tick: a changed list reloads by itself, so the
  // refresh counter only moves when it didn't change.
  const tick = useCallback(() => {
    lastTick.current = Date.now();
    void loadChecks().then((changed) => {
      if (!changed) setRefresh((value) => value + 1);
    });
    void loadAdmin();
    void loadMaintenance();
  }, [loadChecks, loadAdmin, loadMaintenance]);
  const polling = !editing && (POLLED_VIEWS.has(view) || !!openId);
  useVisibleInterval(tick, REFRESH_MS, polling);
  // Back on a results view after a while: catch up at once instead of
  // showing old numbers until the next tick.
  useEffect(() => {
    if (polling && Date.now() - lastTick.current >= REFRESH_MS) tick();
  }, [polling, tick]);

  const allChecks = useMemo(() => checks ?? [], [checks]);
  const result = useMonitorData(allChecks, range, refresh, occurrences, polling);
  const locations = useMemo(() => {
    const names = new Set<string>(probes.map((probe) => probe.location));
    for (const check of allChecks) check.locations.forEach((location) => names.add(location));
    for (const data of result.data.values())
      data.state.locations.forEach((state) => names.add(state.location));
    return [...names].sort();
  }, [probes, allChecks, result.data]);
  const openCheck = allChecks.find((check) => check.id === openId);
  const openMonitor = useCallback((check: CheckView) => setOpenId(check.id), []);
  const statusOf = useCallback(
    (check: CheckView) => result.data.get(check.id)?.state.status ?? 'none',
    [result.data],
  );

  // Stable callbacks, so the memoised views skip renders their data didn't cause.
  const withBusy = useCallback(
    async (key: string, action: () => Promise<unknown>) => {
      setBusy((current) => new Set(current).add(key));
      try {
        await action();
      } catch (reason) {
        message.error((reason as Error).message);
      } finally {
        setBusy((current) => {
          const next = new Set(current);
          next.delete(key);
          return next;
        });
      }
    },
    [message],
  );
  const clone = useCallback(
    (check: CheckView) =>
      withBusy(`clone:${check.id}`, async () => {
        const copy = await syntheticsApi.cloneCheck(check.id);
        message.success(`Created "${copy.name}", disabled until you enable it`);
        await loadChecks();
      }),
    [withBusy, message, loadChecks],
  );
  const toggle = useCallback(
    (check: CheckView) =>
      withBusy(`toggle:${check.id}`, async () => {
        await syntheticsApi.setEnabled(check.id, !check.enabled, check.version);
        message.success(
          check.enabled ? `Disabled "${check.name}": no runs scheduled` : `Enabled "${check.name}"`,
        );
        if (!(await loadChecks())) setRefresh((value) => value + 1);
      }),
    [withBusy, message, loadChecks],
  );
  const remove = useCallback(
    (check: CheckView) =>
      withBusy(`delete:${check.id}`, async () => {
        await syntheticsApi.deleteCheck(check.id);
        message.success(`Deleted "${check.name}"`);
        await loadChecks();
      }),
    [withBusy, message, loadChecks],
  );
  const edit = useCallback(
    (check?: CheckView) => {
      setOpenId(undefined);
      if (canManage && destinations.length === 0) {
        message.warning('Add a destination first: it is where results go.');
        setView('destinations');
        return;
      }
      setEditing(check ?? 'new');
    },
    [canManage, destinations.length, message],
  );
  const endMaintenance = (windowId: string) =>
    withBusy(`end:${windowId}`, async () => {
      const ended = await maintenanceApi.end(windowId);
      message.success(`Ended "${ended.name}". Sites unmute its monitors within 5 minutes.`);
      await loadMaintenance();
    });
  const exportYAML = () =>
    withBusy('export', async () => {
      downloadText(
        'synthetics-checks.yaml',
        await syntheticsApi.exportChecks(),
        'application/yaml',
      );
    });

  if (editing) {
    return (
      <div
        className="w-full overflow-y-auto rounded bg-white p-5 shadow dark:bg-gray-800"
        style={{ height: contentHeight }}
      >
        <MonitorEditorComponent
          check={editing === 'new' ? undefined : editing}
          destinations={destinations}
          locations={locations}
          checks={allChecks}
          channels={channels}
          onClose={() => setEditing(undefined)}
          onSaved={() => {
            setEditing(undefined);
            setView('monitors');
            void loadChecks();
          }}
        />
      </div>
    );
  }

  return (
    <div
      className="w-full overflow-y-auto rounded bg-white p-5 shadow dark:bg-gray-800"
      style={{ height: contentHeight }}
    >
      <div className="mb-2 flex flex-wrap items-center gap-3">
        <h2 className="m-0 text-lg font-semibold">Synthetics</h2>
        <span className="text-xs text-gray-500 dark:text-gray-400">
          Checks run by probes, results stored on Sites
        </span>
        <div className="ml-auto flex flex-wrap items-center gap-2">
          {result.loading && checks && <Spin size="small" indicator={<LoadingOutlined spin />} />}
          <Segmented<RangeKey>
            value={range}
            onChange={setRange}
            options={Object.keys(RANGE_SECONDS) as RangeKey[]}
          />
          {canManage && view === 'monitors' && (
            <>
              <Button icon={<UploadOutlined />} onClick={() => setImporting(true)}>
                Import YAML
              </Button>
              <Button icon={<DownloadOutlined />} onClick={exportYAML} loading={busy.has('export')}>
                Export YAML
              </Button>
            </>
          )}
          {canManage && view !== 'maintenance' && (
            <Button type="primary" icon={<PlusOutlined />} onClick={() => edit()}>
              New monitor
            </Button>
          )}
        </div>
      </div>

      {loadError && (
        <Alert
          type="error"
          showIcon
          className="mb-3"
          title="Couldn't load the monitors"
          description={loadError}
        />
      )}
      {result.errors.length > 0 && (
        <Alert
          type="warning"
          showIcon
          className="mb-3"
          title="Some results couldn't be read"
          description={result.errors.join('; ')}
        />
      )}

      <Tabs
        activeKey={view}
        onChange={(key) => setView(key as View)}
        items={[
          {
            key: 'overview',
            label: 'Overview',
            children: checks ? (
              <OverviewComponent
                checks={allChecks}
                result={result}
                locations={locations}
                onOpen={openMonitor}
              />
            ) : (
              <Loading />
            ),
          },
          {
            key: 'monitors',
            label: `Monitors${checks ? ` (${checks.length})` : ''}`,
            children: checks ? (
              <MonitorsTableComponent
                checks={allChecks}
                data={result.data}
                locations={locations}
                canManage={canManage}
                busy={busy}
                onOpen={openMonitor}
                onEdit={edit}
                onClone={clone}
                onToggle={toggle}
                onDelete={remove}
                scrollHeight={Math.max(360, contentHeight - 220)}
              />
            ) : (
              <Loading />
            ),
          },
          {
            key: 'maintenance',
            label: 'Maintenance',
            children: (
              <MaintenanceComponent
                windows={windows}
                occurrences={occurrences}
                checks={allChecks}
                canManage={canManage}
                onChanged={loadMaintenance}
                editId={editWindowId}
                onEditOpened={() => setEditWindowId(undefined)}
              />
            ),
          },
          {
            key: 'map',
            label: 'Dependency map',
            children: checks ? (
              <DependencyMapComponent checks={allChecks} data={result.data} onOpen={openMonitor} />
            ) : (
              <Loading />
            ),
          },
          ...(canManage
            ? [
                {
                  key: 'probes',
                  label: `Probe groups (${probes.length})`,
                  children: (
                    <ProbesComponent probes={probes} canManage={canManage} onChanged={loadAdmin} />
                  ),
                },
                {
                  key: 'destinations',
                  label: 'Destinations',
                  children: (
                    <DestinationsComponent destinations={destinations} onChanged={loadAdmin} />
                  ),
                },
              ]
            : []),
        ]}
      />

      {openCheck && (
        <MonitorDetailDrawer
          key={openCheck.id}
          check={openCheck}
          monitor={result.data.get(openCheck.id)}
          range={range}
          canManage={canManage}
          onClose={() => setOpenId(undefined)}
          onEdit={edit}
          onClone={clone}
          onToggle={toggle}
          checks={allChecks}
          statusOf={statusOf}
          onOpenCheck={openMonitor}
          refresh={refresh}
          maintenance={occurrences}
          onEndMaintenance={canManage ? endMaintenance : undefined}
          onOpenMaintenance={(windowId) => {
            setEditWindowId(windowId);
            setOpenId(undefined);
            setView('maintenance');
          }}
        />
      )}
      <ImportDrawer
        open={importing}
        onClose={() => setImporting(false)}
        onApplied={() => {
          void loadChecks();
          void loadMaintenance();
        }}
      />
    </div>
  );
};

const Loading = () => (
  <div className="flex justify-center py-16">
    <Spin indicator={<LoadingOutlined spin />} />
  </div>
);
