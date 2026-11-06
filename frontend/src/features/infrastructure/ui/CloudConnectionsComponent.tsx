// Read-only access to AWS accounts and Azure subscriptions, scanned by a probe group.
import {
  DeleteOutlined,
  EditOutlined,
  PlusOutlined,
  ReloadOutlined,
  SettingOutlined,
} from '@ant-design/icons';
import { App, Button, Popconfirm, Table, Tag, Tooltip } from 'antd';
import dayjs from 'dayjs';
import { useCallback, useEffect, useState } from 'react';

import { type CloudConnection, cloudConnectionsApi } from '../../../entity/cloud';
import { type Destination, type Probe, syntheticsApi } from '../../../entity/synthetics';
import { IconAction } from '../../synthetics';
import { HEALTH_COLOR } from '../lib/assets';
import { CloudWizard } from './CloudWizard';
import { ProviderLogo } from './ProviderLogo';

interface Props {
  canManage: boolean;
  /** Read-only views (Infrastructure) link admins to where connections are managed. */
  onManage?: () => void;
}

const USE_LABEL: Record<string, string> = {
  inventory: 'Inventory',
  cost: 'Cost',
  posture: 'Posture',
};

const ACCESS_LABEL: Record<string, string> = {
  probe: "Probe's own identity",
  role: 'Role with external ID',
  workload: 'Workload identity',
  federated: 'Federated via control plane',
};

/** No report within three health intervals: the probe group isn't scanning. */
const isStale = (connection: CloudConnection) =>
  !connection.status.reportedAt ||
  dayjs().diff(dayjs(connection.status.reportedAt), 'second') >
    Math.min(connection.healthIntervalSeconds || 900, connection.intervalSeconds) * 3;

/** "4 h" or "15 min". */
const every = (seconds: number) =>
  seconds % 3600 === 0 ? `${seconds / 3600} h` : `${Math.round(seconds / 60)} min`;

/** How often the cloud asked to slow down in the latest scans. */
const throttling = (connection: CloudConnection) => {
  const scans = [connection.status.scan, connection.status.health].filter(Boolean);
  const throttled = scans.reduce((sum, scan) => sum + (scan?.throttled ?? 0), 0);
  const waited = scans.reduce((sum, scan) => sum + (scan?.stoodDownSeconds ?? 0), 0);
  return throttled
    ? `The cloud throttled ${throttled} calls in the latest scans; the probe waited ${Math.round(waited)} s as asked.`
    : undefined;
};

const statusOf = (
  connection: CloudConnection,
): { color: string; label: string; detail?: string } => {
  if (!connection.enabled) return { color: HEALTH_COLOR.inactive, label: 'Disabled' };
  const scan = connection.status.scan;
  if (!scan) return { color: HEALTH_COLOR.unknown, label: 'Waiting for the first discovery' };
  const uses = [scan, connection.status.health].flatMap((each) =>
    each ? Object.values(each.uses) : [],
  );
  const error = uses.find((use) => use.state === 'error');
  if (error) return { color: HEALTH_COLOR.critical, label: 'Failing', detail: error.message };
  const missing = uses.find((use) => use.state === 'needs_permission');
  if (missing) {
    return {
      color: HEALTH_COLOR.warning,
      label: 'Needs permission',
      detail: `${missing.message ?? ''} Missing: ${(missing.missing ?? []).join(', ')}`,
    };
  }
  if (isStale(connection)) {
    return {
      color: HEALTH_COLOR.warning,
      label: 'Late',
      detail: 'No scan reported for three intervals',
    };
  }
  return { color: HEALTH_COLOR.healthy, label: 'Scanning', detail: throttling(connection) };
};

export const CloudConnectionsComponent = ({ canManage, onManage }: Props) => {
  const { message } = App.useApp();
  const [connections, setConnections] = useState<CloudConnection[]>();
  const [probes, setProbes] = useState<Probe[]>([]);
  const [destinations, setDestinations] = useState<Destination[]>([]);
  const [editing, setEditing] = useState<CloudConnection | 'new'>();

  const load = useCallback(async () => {
    try {
      setConnections(await cloudConnectionsApi.list());
      if (canManage) {
        const [nextProbes, nextDestinations] = await Promise.all([
          syntheticsApi.listProbes(),
          syntheticsApi.listDestinations(),
        ]);
        setProbes(nextProbes);
        setDestinations(nextDestinations);
      }
    } catch (error) {
      message.error((error as Error).message);
      setConnections((current) => current ?? []);
    }
  }, [canManage, message]);

  useEffect(() => {
    void load();
  }, [load]);

  const remove = async (connection: CloudConnection) => {
    try {
      await cloudConnectionsApi.remove(connection.id);
      message.success(
        `Deleted "${connection.name}"; its resources leave Infrastructure as they age out`,
      );
      void load();
    } catch (error) {
      message.error((error as Error).message);
    }
  };

  return (
    <div className="flex flex-col gap-3">
      <div className="flex flex-wrap items-center gap-3">
        <span className="max-w-3xl flex-1 text-sm text-gray-500 dark:text-gray-400">
          Read-only access to AWS accounts and Azure subscriptions. A probe group scans each one and
          its resources join Assets, linked to what the agents report. Obzly stores only
          identifiers, never cloud keys; the same connection will serve cost and posture later.
        </span>
        <Button icon={<ReloadOutlined />} onClick={() => void load()}>
          Refresh
        </Button>
        {canManage && (
          <Button type="primary" icon={<PlusOutlined />} onClick={() => setEditing('new')}>
            New cloud connection
          </Button>
        )}
        {onManage && (
          <Button icon={<SettingOutlined />} onClick={onManage}>
            Manage in Obzly settings
          </Button>
        )}
      </div>
      <Table<CloudConnection>
        rowKey="id"
        size="middle"
        loading={!connections}
        dataSource={connections ?? []}
        pagination={false}
        scroll={{ x: 1250 }}
        locale={{
          emptyText: canManage
            ? 'No cloud connections yet. Add one to inventory resources that run no agent.'
            : onManage
              ? 'No cloud connections yet. Add one in Obzly settings → Cloud connections.'
              : 'No cloud connections yet. An admin adds them in Obzly settings.',
        }}
        columns={[
          {
            title: 'Status',
            key: 'status',
            width: 170,
            render: (_, connection) => {
              const status = statusOf(connection);
              return (
                <Tooltip title={status.detail}>
                  <span className="inline-flex items-center gap-2 text-sm">
                    <span
                      className="inline-block h-2.5 w-2.5 rounded-full"
                      style={{ background: status.color }}
                    />
                    {status.label}
                  </span>
                </Tooltip>
              );
            },
          },
          {
            title: 'Connection',
            key: 'name',
            render: (_, connection) => (
              <span className="inline-flex items-center gap-2">
                <ProviderLogo provider={connection.provider} />
                <span>
                  <span className="font-medium">{connection.name}</span>
                  <span className="block font-mono text-xs text-gray-500 dark:text-gray-400">
                    {connection.scope.replace('_', ' ')} {connection.scopeId}
                  </span>
                </span>
              </span>
            ),
          },
          {
            title: 'Scan types',
            key: 'uses',
            width: 130,
            render: (_, connection) =>
              connection.scanTypes.map((use) => <Tag key={use}>{USE_LABEL[use] ?? use}</Tag>),
          },
          {
            title: 'Access',
            key: 'access',
            width: 190,
            render: (_, connection) => (
              <span className="text-xs">{ACCESS_LABEL[connection.access]}</span>
            ),
          },
          {
            title: 'Probe group',
            key: 'probe',
            width: 150,
            render: (_, connection) => (
              <span className="font-mono text-xs">{connection.probe.name || '—'}</span>
            ),
          },
          {
            title: 'Regions',
            key: 'regions',
            width: 160,
            render: (_, connection) => {
              const scanned = connection.status.scan?.regions ?? [];
              return (
                <span className="text-xs">
                  {connection.regions.length > 0
                    ? connection.regions.join(', ')
                    : `all enabled${scanned.length ? ` (${scanned.length} with resources)` : ''}`}
                </span>
              );
            },
          },
          {
            title: 'Resources',
            key: 'resources',
            width: 100,
            align: 'right',
            render: (_, connection) => {
              const scan = connection.status.scan;
              if (!scan) return '—';
              const types = Object.entries(scan.types ?? {})
                .sort(([, a], [, b]) => b - a)
                .map(([type, count]) => `${type.replace(/^(aws|azure)_/, '')}: ${count}`)
                .join(', ');
              return <Tooltip title={types}>{scan.resources.toLocaleString()}</Tooltip>;
            },
          },
          {
            title: 'Schedule',
            key: 'schedule',
            width: 150,
            render: (_, connection) => (
              <span className="text-xs">
                Discovery every {every(connection.intervalSeconds)}
                <span className="block text-gray-500 dark:text-gray-400">
                  Health every {every(connection.healthIntervalSeconds || 900)}
                </span>
              </span>
            ),
          },
          {
            title: 'Last scans',
            key: 'reported',
            width: 160,
            render: (_, connection) => {
              const { scan, health } = connection.status;
              if (!scan && !health) return '—';
              const when = (each?: { startedAt: string }) =>
                each ? (
                  <Tooltip title={dayjs(each.startedAt).format('D MMM YYYY HH:mm:ss')}>
                    {dayjs(each.startedAt).fromNow()}
                  </Tooltip>
                ) : (
                  '—'
                );
              return (
                <span className="text-xs">
                  Discovery {when(scan)}
                  <span className="block text-gray-500 dark:text-gray-400">
                    Health {when(health)}
                  </span>
                </span>
              );
            },
          },
          ...(canManage
            ? [
                {
                  title: 'Actions',
                  key: 'actions',
                  width: 90,
                  render: (_: unknown, connection: CloudConnection) => (
                    <span className="inline-flex gap-0.5">
                      <IconAction
                        title="Edit"
                        icon={<EditOutlined />}
                        onClick={() => setEditing(connection)}
                      />
                      <Popconfirm
                        title={`Delete "${connection.name}"?`}
                        description="Scans stop. Nothing changes in the cloud account; remove the role or identity there yourself."
                        okText="Delete"
                        okButtonProps={{ danger: true }}
                        onConfirm={() => remove(connection)}
                      >
                        <span>
                          <IconAction title="Delete" icon={<DeleteOutlined />} danger />
                        </span>
                      </Popconfirm>
                    </span>
                  ),
                },
              ]
            : []),
        ]}
      />
      {editing && (
        <CloudWizard
          connection={editing === 'new' ? undefined : editing}
          probes={probes}
          destinations={destinations}
          onClose={() => setEditing(undefined)}
          onSaved={() => {
            setEditing(undefined);
            void load();
          }}
        />
      )}
    </div>
  );
};
