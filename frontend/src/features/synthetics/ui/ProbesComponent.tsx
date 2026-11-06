import { CopyOutlined, DeleteOutlined, KeyOutlined, PlusOutlined } from '@ant-design/icons';
import {
  Alert,
  App,
  Button,
  Checkbox,
  Input,
  Modal,
  Popconfirm,
  Segmented,
  Table,
  Tag,
  Tooltip,
} from 'antd';
import { useState } from 'react';

import { APP_VERSION, getApplicationServer } from '../../../constants';
import {
  type Probe,
  type ProbeMember,
  type ProbeTokenResponse,
  syntheticsApi,
} from '../../../entity/synthetics';
import { copyToClipboard } from '../../../shared/lib';
import { IconAction, StatusDot } from './parts';

interface Props {
  probes: Probe[];
  canManage: boolean;
  onChanged: () => void;
}

type InstallKind = 'linux' | 'docker' | 'helm';

const RECENT_STOP_MS = 10 * 60_000;

const releaseVersion = () => (APP_VERSION && /^\d/.test(APP_VERSION) ? APP_VERSION : '');

const installSnippet = (kind: InstallKind, token: string): string => {
  const controlPlane = getApplicationServer() || window.location.origin;
  const version = releaseVersion();
  const tag = version ? `v${version}` : 'latest';
  switch (kind) {
    case 'linux':
      return [
        `curl -fsSL https://github.com/obzly/obzly/releases/${version ? `download/v${version}` : 'latest/download'}/install.sh | \\`,
        `  sudo OBZLY_CONTROL_PLANE_URL=${controlPlane} OBZLY_PROBE_TOKEN=${token} sh`,
      ].join('\n');
    case 'docker':
      return [
        'docker run -d --name obzly-probe --restart unless-stopped \\',
        `  -e OBZLY_CONTROL_PLANE_URL=${controlPlane} \\`,
        `  -e OBZLY_PROBE_TOKEN=${token} \\`,
        '  -v obzly-probe:/var/lib/obzly-probe \\',
        `  ghcr.io/obzly/obzly-probe:${tag}`,
      ].join('\n');
    default:
      return [
        'helm upgrade --install obzly-probe deploy/helm/obzly-probe \\',
        '  --namespace obzly-probe --create-namespace \\',
        `  --set controlPlaneUrl=${controlPlane} \\`,
        `  --set token=${token} \\`,
        '  --set replicas=3',
      ].join('\n');
  }
};

export const ProbesComponent = ({ probes, canManage, onChanged }: Props) => {
  const { message } = App.useApp();
  const [adding, setAdding] = useState(false);
  const [form, setForm] = useState({ name: '', location: '', description: '' });
  const [created, setCreated] = useState<ProbeTokenResponse>();
  const [revokeOld, setRevokeOld] = useState(false);
  const [install, setInstall] = useState<InstallKind>('linux');
  const [saving, setSaving] = useState(false);

  const create = async () => {
    setSaving(true);
    try {
      const response = await syntheticsApi.createProbe(form.name, form.location, form.description);
      setCreated(response);
      setAdding(false);
      setForm({ name: '', location: '', description: '' });
      onChanged();
    } catch (reason) {
      message.error((reason as Error).message);
    } finally {
      setSaving(false);
    }
  };

  const rotate = async (probe: Probe) => {
    try {
      setCreated(await syntheticsApi.rotateProbeToken(probe.id, revokeOld));
      message.success(
        revokeOld
          ? `New token for ${probe.name}; the old one no longer works`
          : `New token for ${probe.name}; the old one keeps working for 24 h while you update its instances`,
      );
    } catch (reason) {
      message.error((reason as Error).message);
    }
  };

  const remove = async (probe: Probe) => {
    try {
      await syntheticsApi.deleteProbe(probe.id);
      message.success(`Probe group ${probe.name} deleted`);
      onChanged();
    } catch (reason) {
      message.error((reason as Error).message);
    }
  };

  const snippet = created ? installSnippet(install, created.token) : '';

  return (
    <div>
      {canManage && (
        <div className="mb-3 flex justify-end">
          <Button type="primary" icon={<PlusOutlined />} onClick={() => setAdding(true)}>
            Add probe group
          </Button>
        </div>
      )}
      <p className="mt-0 mb-3 text-xs text-gray-500 dark:text-gray-400">
        A probe group is one location with one token. Run as many instances with that token as you
        like: each monitor runs on one live instance, and moves to another within about a minute if
        it stops.
      </p>
      <Table<Probe>
        rowKey="id"
        size="middle"
        dataSource={probes}
        pagination={false}
        scroll={{ x: 900 }}
        expandable={{
          rowExpandable: (probe) => (probe.members ?? []).length > 0,
          expandedRowRender: (probe) => <Members members={probe.members ?? []} />,
        }}
        locale={{
          emptyText:
            'No probe groups yet. Add one, then install its instances where you want to test from.',
        }}
        columns={[
          {
            title: 'Status',
            key: 'online',
            width: 150,
            render: (_, probe) => {
              const members = probe.members ?? [];
              const live = members.filter((member) => member.live).length;
              if (members.length === 0) {
                return (
                  <span className="inline-flex items-center gap-2">
                    <StatusDot status={probe.online ? 'up' : 'down'} />
                    {probe.online ? 'Online' : 'Offline'}
                  </span>
                );
              }
              // Rollouts rename instances, so only a recent stop is a problem.
              const stopped = members.filter(
                (member) =>
                  !member.live && Date.now() - Date.parse(member.lastSeenAt) < RECENT_STOP_MS,
              ).length;
              return (
                <Tooltip
                  title={
                    stopped > 0
                      ? `${stopped} instance${stopped === 1 ? '' : 's'} stopped in the last 10 minutes; their monitors moved to the others`
                      : undefined
                  }
                >
                  <span className="inline-flex items-center gap-2">
                    <StatusDot status={live === 0 ? 'down' : stopped > 0 ? 'warn' : 'up'} />
                    {live === 0 ? 'No live instance' : `${live} live`}
                    {stopped > 0 && `, ${stopped} stopped`}
                  </span>
                </Tooltip>
              );
            },
          },
          {
            title: 'Location',
            dataIndex: 'location',
            render: (value: string) => <span className="font-mono text-xs">{value}</span>,
          },
          { title: 'Group', dataIndex: 'name' },
          {
            title: 'Version',
            dataIndex: 'version',
            render: (value: string) => (
              <span className="font-mono text-xs">
                {value || '—'}
                {value && releaseVersion() && value !== releaseVersion() && (
                  <Tag color="warning" className="ml-2">
                    update
                  </Tag>
                )}
              </span>
            ),
          },
          {
            title: 'Last seen',
            dataIndex: 'lastSeenAt',
            render: (value: string | null) => (value ? new Date(value).toLocaleString() : 'never'),
          },
          { title: 'Monitors', dataIndex: 'checks', align: 'right' },
          ...(canManage
            ? [
                {
                  title: 'Actions',
                  key: 'actions',
                  width: 90,
                  render: (_: unknown, probe: Probe) => (
                    <span className="inline-flex gap-0.5">
                      <Popconfirm
                        title={`Replace ${probe.name}'s token?`}
                        description={
                          <div className="max-w-xs">
                            <div>
                              Its running instances keep the old token for 24 h; update them to the
                              new one before then.
                            </div>
                            <Checkbox
                              className="mt-2"
                              checked={revokeOld}
                              onChange={(event) => setRevokeOld(event.target.checked)}
                            >
                              Revoke the old token now (it leaked)
                            </Checkbox>
                          </div>
                        }
                        okText="Replace"
                        onOpenChange={(open) => open && setRevokeOld(false)}
                        onConfirm={() => rotate(probe)}
                      >
                        <span>
                          <IconAction title="Replace token" icon={<KeyOutlined />} />
                        </span>
                      </Popconfirm>
                      <Popconfirm
                        title={`Delete probe group ${probe.name}?`}
                        description="Its instances stop running monitors."
                        okText="Delete"
                        okButtonProps={{ danger: true }}
                        onConfirm={() => remove(probe)}
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

      <Modal
        open={adding}
        title="Add a probe group"
        okText="Create"
        confirmLoading={saving}
        okButtonProps={{ disabled: !form.name.trim() || !form.location.trim() }}
        onOk={create}
        onCancel={() => setAdding(false)}
      >
        <div className="flex flex-col gap-3">
          <label className="flex flex-col gap-1 text-xs font-medium">
            Name
            <Input
              value={form.name}
              placeholder="aws-ap-south-1"
              onChange={(event) => setForm({ ...form, name: event.target.value })}
            />
          </label>
          <label className="flex flex-col gap-1 text-xs font-medium">
            Location
            <Input
              className="font-mono"
              value={form.location}
              placeholder="aws-ap-south-1"
              onChange={(event) => setForm({ ...form, location: event.target.value.toLowerCase() })}
            />
            <span className="font-normal text-gray-500 dark:text-gray-400">
              Monitors pick where they run by this label. One group per location.
            </span>
          </label>
          <label className="flex flex-col gap-1 text-xs font-medium">
            Description
            <Input
              value={form.description}
              onChange={(event) => setForm({ ...form, description: event.target.value })}
            />
          </label>
        </div>
      </Modal>

      <Modal
        open={!!created}
        title={`Install ${created?.probe.name ?? ''}`}
        footer={null}
        width={720}
        onCancel={() => setCreated(undefined)}
      >
        <Alert
          type="warning"
          showIcon
          className="mb-3"
          title="Copy the token now: it is shown only once."
        />
        <Segmented<InstallKind>
          className="mb-3"
          value={install}
          onChange={setInstall}
          options={[
            { value: 'linux', label: 'Linux (systemd)' },
            { value: 'docker', label: 'Docker' },
            { value: 'helm', label: 'Kubernetes (Helm)' },
          ]}
        />
        <pre className="m-0 overflow-x-auto rounded bg-gray-50 p-3 font-mono text-xs dark:bg-gray-900">
          {snippet}
        </pre>
        <p className="mt-2 mb-0 text-xs text-gray-500 dark:text-gray-400">
          {install === 'helm'
            ? 'Three replicas by default, spread over zones and nodes; change replicas for more or fewer.'
            : 'Run the same command on more machines to add instances to the group; each names itself after its host.'}
        </p>
        <div className="mt-3 flex justify-end">
          <Button
            icon={<CopyOutlined />}
            onClick={() => copyToClipboard(snippet).then(() => message.success('Copied'))}
          >
            Copy
          </Button>
        </div>
      </Modal>
    </div>
  );
};

/** A group's instances, newest heard first. */
const Members = ({ members }: { members: ProbeMember[] }) => (
  <Table<ProbeMember>
    rowKey="name"
    size="small"
    pagination={false}
    dataSource={[...members].sort((a, b) => b.lastSeenAt.localeCompare(a.lastSeenAt))}
    columns={[
      {
        title: 'Instance',
        dataIndex: 'name',
        render: (name: string, member) => (
          <span className="inline-flex items-center gap-2 font-mono text-xs">
            <StatusDot status={member.live ? 'up' : 'none'} size={8} />
            {name}
          </span>
        ),
      },
      {
        title: 'State',
        key: 'live',
        render: (_, member) => (member.live ? 'Live' : 'Gone (not heard from in a minute)'),
      },
      {
        title: 'Version',
        dataIndex: 'version',
        render: (value: string) => <span className="font-mono text-xs">{value || '—'}</span>,
      },
      { title: 'Monitors', dataIndex: 'checks', align: 'right' },
      {
        title: 'First seen',
        dataIndex: 'firstSeen',
        render: (value: string) => new Date(value).toLocaleString(),
      },
      {
        title: 'Last seen',
        dataIndex: 'lastSeenAt',
        render: (value: string) => new Date(value).toLocaleString(),
      },
    ]}
  />
);
