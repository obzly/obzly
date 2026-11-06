// Central on a control plane (Sites pull them); an enrolled Site shows them
// read-only; a standalone Site keeps its own.
import {
  DeleteOutlined,
  EditOutlined,
  ExportOutlined,
  LoadingOutlined,
  PlusOutlined,
  SendOutlined,
} from '@ant-design/icons';
import {
  Alert,
  App,
  Button,
  Input,
  InputNumber,
  Modal,
  Popconfirm,
  Radio,
  Select,
  Spin,
  Table,
  Tag,
  Tooltip,
} from 'antd';
import { useCallback, useEffect, useState } from 'react';

import { IS_CONTROL_PLANE } from '../../../constants';
import {
  type ChannelScope,
  type SMTPRequest,
  type SMTPSettings,
  type SettingsChannel,
  type SettingsSync,
  notificationSettingsApi,
} from '../../../entity/alerting';
import { projectApi } from '../../../entity/projects/api/projectApi';
import { useSite } from '../../../shared/site';
import { type Draft, KINDS, configOf, emptyDraft } from './channelDraft';
import { errorMessage } from './lib';

type ScopeMode = 'all' | 'sites' | 'projects';

interface ChannelDraft extends Draft {
  scopeMode: ScopeMode;
  sites: string[];
  /** "siteId/projectId" on a control plane, projectId on a Site. */
  projects: string[];
}

const emptySMTP: SMTPRequest = {
  host: '',
  port: 587,
  username: '',
  password: '',
  from: '',
  tls: 'starttls',
};

export const NotificationSettingsComponent = () => {
  const { message, modal } = App.useApp();
  const { sites } = useSite();
  const [smtp, setSmtp] = useState<SMTPSettings>();
  const [smtpDraft, setSmtpDraft] = useState<SMTPRequest>(emptySMTP);
  const [testTo, setTestTo] = useState('');
  const [channels, setChannels] = useState<SettingsChannel[]>([]);
  const [sync, setSync] = useState<SettingsSync>();
  const [draft, setDraft] = useState<ChannelDraft | null>(null);
  const [projectOptions, setProjectOptions] = useState<{ value: string; label: string }[]>([]);
  const [busy, setBusy] = useState<string | null>(null);
  const [loading, setLoading] = useState(true);

  const managed = sync?.mode === 'site';

  const load = useCallback(async () => {
    try {
      const [nextSmtp, nextChannels, nextSync] = await Promise.all([
        notificationSettingsApi.getSMTP(),
        notificationSettingsApi.listChannels(),
        notificationSettingsApi.getSync(),
      ]);
      setSmtp(nextSmtp);
      setSmtpDraft(
        nextSmtp.configured
          ? {
              host: nextSmtp.host,
              port: nextSmtp.port,
              username: nextSmtp.username,
              password: '',
              from: nextSmtp.from,
              tls: nextSmtp.tls,
            }
          : emptySMTP,
      );
      setChannels(nextChannels);
      setSync(nextSync);
    } catch (error) {
      message.error(errorMessage(error, 'Failed to load email and channel settings'));
    } finally {
      setLoading(false);
    }
  }, [message]);

  useEffect(() => {
    void load();
  }, [load]);

  // Projects to scope a channel to: every Site's on a control plane, this one's otherwise.
  useEffect(() => {
    if (!draft || draft.scopeMode !== 'projects') return;
    const load = async () => {
      if (IS_CONTROL_PLANE) {
        const lists = await Promise.all(
          sites.map(async (site) => {
            try {
              const response = await projectApi.getProjectsOnSite(site.id);
              return response.projects.map((project) => ({
                value: `${site.id}/${project.id}`,
                label: `${site.displayName || site.id} / ${project.name}`,
              }));
            } catch {
              return [];
            }
          }),
        );
        setProjectOptions(lists.flat());
      } else {
        const response = await projectApi.getProjects();
        setProjectOptions(
          response.projects.map((project) => ({ value: project.id, label: project.name })),
        );
      }
    };
    void load();
  }, [draft?.scopeMode, sites]);

  const run = async (key: string, action: () => Promise<unknown>, success: string) => {
    setBusy(key);
    try {
      await action();
      message.success(success);
      await load();
    } catch (error) {
      message.error(errorMessage(error, 'That didn’t work'));
    } finally {
      setBusy(null);
    }
  };

  const saveChannel = async () => {
    if (!draft) return;
    const request = {
      name: draft.name,
      kind: draft.kind,
      config: configOf(draft),
      ...(IS_CONTROL_PLANE
        ? { scope: scopeOf(draft) }
        : {
            allProjects: draft.scopeMode === 'all',
            projectIds: draft.scopeMode === 'projects' ? draft.projects : [],
          }),
    };
    await run(
      'channel',
      () =>
        draft.id
          ? notificationSettingsApi.updateChannel(draft.id, request)
          : notificationSettingsApi.createChannel(request),
      draft.id ? 'Channel updated' : 'Channel created',
    );
    setDraft(null);
  };

  const edit = (channel: SettingsChannel) => {
    const scope = channel.scope;
    setDraft({
      ...emptyDraft,
      id: channel.id,
      name: channel.name,
      kind: channel.kind,
      scopeMode: IS_CONTROL_PLANE
        ? scope?.allSites
          ? 'all'
          : scope?.sites?.length
            ? 'sites'
            : 'projects'
        : channel.allProjects
          ? 'all'
          : 'projects',
      sites: scope?.sites ?? [],
      projects: IS_CONTROL_PLANE
        ? (scope?.projects ?? []).map((project) => `${project.siteId}/${project.projectId}`)
        : channel.projectIds,
    });
  };

  if (loading) return <Spin indicator={<LoadingOutlined spin />} />;

  const set = (changes: Partial<ChannelDraft>) =>
    setDraft((current) => (current ? { ...current, ...changes } : current));
  const editing = !!draft?.id;

  return (
    <div className="flex flex-col gap-8">
      <section>
        <h2 className="mb-1 text-xl font-bold">Email</h2>
        <p className="mb-3 max-w-2xl text-sm text-gray-500 dark:text-gray-300">
          The SMTP relay Obzly sends alert emails and invites through.
          {IS_CONTROL_PLANE && ' Every Site uses it too; they pick changes up within 5 minutes.'}
        </p>
        {managed && (
          <Alert
            type="info"
            showIcon
            className="mb-3 max-w-2xl"
            title={
              smtp?.configured
                ? `Managed on the control plane (${smtp.host})`
                : 'Managed on the control plane, not set up there yet'
            }
          />
        )}
        {!managed && (
          <div className="grid max-w-2xl gap-3 sm:grid-cols-2">
            <Labelled label="SMTP host">
              <Input
                value={smtpDraft.host}
                placeholder="email-smtp.eu-west-1.amazonaws.com"
                onChange={(event) => setSmtpDraft({ ...smtpDraft, host: event.target.value })}
              />
            </Labelled>
            <div className="grid grid-cols-2 gap-3">
              <Labelled label="Port">
                <InputNumber
                  className="w-full"
                  min={1}
                  max={65535}
                  value={smtpDraft.port}
                  onChange={(port) => setSmtpDraft({ ...smtpDraft, port: port ?? 587 })}
                />
              </Labelled>
              <Labelled label="TLS">
                <Select
                  value={smtpDraft.tls}
                  options={[
                    { value: 'starttls', label: 'STARTTLS' },
                    { value: 'tls', label: 'Implicit TLS' },
                  ]}
                  onChange={(tls) => setSmtpDraft({ ...smtpDraft, tls })}
                />
              </Labelled>
            </div>
            <Labelled label="Username">
              <Input
                value={smtpDraft.username}
                autoComplete="off"
                onChange={(event) => setSmtpDraft({ ...smtpDraft, username: event.target.value })}
              />
            </Labelled>
            <Labelled label="Password">
              <Input.Password
                value={smtpDraft.password}
                autoComplete="new-password"
                placeholder={smtp?.passwordSet ? 'stored (type to replace)' : ''}
                onChange={(event) => setSmtpDraft({ ...smtpDraft, password: event.target.value })}
              />
            </Labelled>
            <Labelled label="From address">
              <Input
                value={smtpDraft.from}
                placeholder="alerts@example.com"
                onChange={(event) => setSmtpDraft({ ...smtpDraft, from: event.target.value })}
              />
            </Labelled>
            <div className="flex items-end gap-2">
              <Button
                type="primary"
                loading={busy === 'smtp'}
                onClick={() =>
                  run(
                    'smtp',
                    () => notificationSettingsApi.saveSMTP(smtpDraft),
                    'Email settings saved',
                  )
                }
              >
                Save
              </Button>
              {smtp?.configured && (
                <Popconfirm
                  title="Remove the email settings? Email channels and invites stop sending."
                  okText="Remove"
                  okButtonProps={{ danger: true }}
                  onConfirm={() =>
                    run('smtp', notificationSettingsApi.deleteSMTP, 'Email settings removed')
                  }
                >
                  <Button danger>Remove</Button>
                </Popconfirm>
              )}
            </div>
          </div>
        )}
        {smtp?.configured && (
          <div className="mt-3 flex max-w-2xl gap-2">
            <Input
              placeholder="Send a test to (default: you)"
              value={testTo}
              onChange={(event) => setTestTo(event.target.value)}
            />
            <Button
              icon={<SendOutlined />}
              loading={busy === 'smtp-test'}
              onClick={() =>
                run('smtp-test', () => notificationSettingsApi.testSMTP(testTo), 'Test email sent')
              }
            >
              Send test email
            </Button>
          </div>
        )}
      </section>

      <section>
        <div className="mb-1 flex items-center justify-between gap-3">
          <h2 className="m-0 text-xl font-bold">Notification channels</h2>
          {!managed && (
            <Button
              type="primary"
              icon={<PlusOutlined />}
              onClick={() => setDraft({ ...emptyDraft, scopeMode: 'all', sites: [], projects: [] })}
            >
              New channel
            </Button>
          )}
        </div>
        <p className="mb-3 max-w-2xl text-sm text-gray-500 dark:text-gray-300">
          Define each Slack, webhook, PagerDuty or email destination once; alert rules, Synthetics
          and Infrastructure alerts pick from these.
          {managed && ' This Site gets them from its control plane.'}
        </p>
        <Table<SettingsChannel>
          rowKey="id"
          size="middle"
          dataSource={channels}
          pagination={false}
          locale={{ emptyText: 'No channels yet' }}
          columns={[
            { title: 'Name', dataIndex: 'name' },
            {
              title: 'Kind',
              dataIndex: 'kind',
              width: 110,
              render: (kind: string) => <Tag>{kind}</Tag>,
            },
            { title: 'Sends to', dataIndex: 'target' },
            { title: 'Available to', key: 'scope', render: (_, channel) => scopeText(channel) },
            ...(IS_CONTROL_PLANE
              ? []
              : [
                  {
                    title: 'Used by',
                    key: 'usedBy',
                    width: 100,
                    render: (_: unknown, channel: SettingsChannel) =>
                      `${channel.usedBy} rule${channel.usedBy === 1 ? '' : 's'}`,
                  },
                ]),
            ...(IS_CONTROL_PLANE
              ? []
              : [
                  {
                    title: 'Managed by',
                    key: 'source',
                    width: 130,
                    render: (_: unknown, channel: SettingsChannel) =>
                      channel.source === 'central' ? 'Control plane' : 'This Site',
                  },
                ]),
            {
              title: '',
              key: 'actions',
              width: 120,
              render: (_, channel) => (
                <span className="inline-flex gap-0.5">
                  <Tooltip title="Send test">
                    <Button
                      type="text"
                      size="small"
                      aria-label="Send test"
                      icon={<SendOutlined />}
                      loading={busy === `test:${channel.id}`}
                      onClick={() =>
                        run(
                          `test:${channel.id}`,
                          () => notificationSettingsApi.testChannel(channel.id),
                          `Test notification sent to ${channel.name}`,
                        )
                      }
                    />
                  </Tooltip>
                  {!managed && (
                    <>
                      <Tooltip title="Edit">
                        <Button
                          type="text"
                          size="small"
                          aria-label="Edit"
                          icon={<EditOutlined />}
                          onClick={() => edit(channel)}
                        />
                      </Tooltip>
                      <Popconfirm
                        title={`Delete ${channel.name}? Rules stop notifying it.`}
                        okText="Delete"
                        okButtonProps={{ danger: true }}
                        onConfirm={() =>
                          run(
                            `delete:${channel.id}`,
                            () => notificationSettingsApi.deleteChannel(channel.id),
                            'Channel deleted',
                          )
                        }
                      >
                        <Tooltip title="Delete">
                          <Button
                            type="text"
                            size="small"
                            danger
                            aria-label="Delete"
                            icon={<DeleteOutlined />}
                          />
                        </Tooltip>
                      </Popconfirm>
                    </>
                  )}
                </span>
              ),
            },
          ]}
        />
      </section>

      {sync?.mode === 'control-plane' && (
        <section>
          <h2 className="mb-1 text-xl font-bold">Sites</h2>
          <p className="mb-3 max-w-2xl text-sm text-gray-500 dark:text-gray-300">
            Each Site fetches these settings every 5 minutes and keeps the last copy if the control
            plane is unreachable. &quot;Move here&quot; takes over a Site&apos;s own channels (same
            ids, so its rules keep working) and its email relay if none is set here.
          </p>
          <Table
            rowKey="siteId"
            size="small"
            pagination={false}
            dataSource={sync.sites ?? []}
            columns={[
              {
                title: 'Site',
                dataIndex: 'siteId',
                render: (value: string) => <span className="font-mono text-xs">{value}</span>,
              },
              {
                title: 'Last fetched',
                dataIndex: 'fetchedAt',
                render: (value: string | null) =>
                  value ? new Date(value).toLocaleString() : 'never',
              },
              {
                title: 'Status',
                key: 'current',
                render: (_, site) =>
                  site.current ? (
                    <Tooltip
                      title={
                        site.appliedAt
                          ? `Reported ${new Date(site.appliedAt).toLocaleString()}`
                          : undefined
                      }
                    >
                      <Tag color="success">up to date</Tag>
                    </Tooltip>
                  ) : (
                    <Tag color="warning">update pending</Tag>
                  ),
              },
              {
                title: 'Site key',
                key: 'key',
                render: (_, site) =>
                  site.pendingKeyFingerprint ? (
                    <Popconfirm
                      title={`Accept ${site.siteId}'s new key?`}
                      description={
                        <div className="max-w-sm text-xs">
                          <p>
                            Only if the Site was restored or rebuilt. Check that the Site&apos;s own
                            Notification channels page shows this fingerprint:
                          </p>
                          <p className="font-mono break-all">{site.pendingKeyFingerprint}</p>
                        </div>
                      }
                      okText="Accept"
                      onConfirm={() =>
                        run(
                          `key:${site.siteId}`,
                          () =>
                            notificationSettingsApi.acceptSiteKey(
                              site.siteId,
                              site.pendingKeyFingerprint ?? '',
                            ),
                          'Key accepted',
                        )
                      }
                    >
                      <Button size="small" danger loading={busy === `key:${site.siteId}`}>
                        Key changed - accept?
                      </Button>
                    </Popconfirm>
                  ) : site.keyFingerprint ? (
                    <Tooltip title={site.keyFingerprint}>
                      <span className="font-mono text-xs">{site.keyFingerprint.slice(0, 19)}…</span>
                    </Tooltip>
                  ) : (
                    <span className="text-gray-500 dark:text-gray-300">not read yet</span>
                  ),
              },
              {
                title: '',
                key: 'move',
                width: 200,
                render: (_, site) => (
                  <Popconfirm
                    title={`Move ${site.siteId}'s own channels and email settings here?`}
                    okText="Move"
                    onConfirm={() =>
                      run(
                        `move:${site.siteId}`,
                        async () => {
                          const result = await notificationSettingsApi.moveToCentral(site.siteId);
                          modal.info({
                            title: `Moved from ${site.siteId}`,
                            content: `${result.channels.length} channel(s)${result.smtp ? ' and its email relay' : ''}${result.smtpSkipped ? '; its email relay was not moved (one is already set here)' : ''}.`,
                          });
                        },
                        'Settings moved',
                      )
                    }
                  >
                    <Button
                      size="small"
                      icon={<ExportOutlined />}
                      loading={busy === `move:${site.siteId}`}
                    >
                      Move Site&apos;s own here
                    </Button>
                  </Popconfirm>
                ),
              },
            ]}
          />
        </section>
      )}
      {sync?.mode === 'site' && (
        <div className="text-sm text-gray-500 dark:text-gray-300">
          {sync.applied
            ? `Last updated from the control plane ${new Date(sync.applied.fetchedAt).toLocaleString()} (version ${sync.applied.version}).`
            : 'Not updated from the control plane yet.'}
          {sync.keyFingerprint && (
            <div>
              This Site&apos;s key:{' '}
              <span className="font-mono break-all">{sync.keyFingerprint}</span>
            </div>
          )}
        </div>
      )}

      <Modal
        open={!!draft}
        title={editing ? `Edit ${draft?.name}` : 'New channel'}
        onCancel={() => setDraft(null)}
        onOk={saveChannel}
        okText="Save"
        confirmLoading={busy === 'channel'}
        okButtonProps={{
          disabled: !draft?.name.trim() || (!editing && !configOf(draft ?? emptyDraft)),
        }}
      >
        {draft && (
          <div className="flex flex-col gap-3">
            <Input
              placeholder="Name"
              value={draft.name}
              onChange={(event) => set({ name: event.target.value })}
            />
            <Select
              value={draft.kind}
              disabled={editing}
              onChange={(kind) => set({ kind })}
              options={KINDS}
            />
            {editing && (
              <div className="text-xs text-gray-500 dark:text-gray-300">
                Leave the settings below empty to keep the stored ones.
              </div>
            )}
            {(draft.kind === 'slack' || draft.kind === 'webhook') && (
              <Input
                placeholder={
                  draft.kind === 'slack'
                    ? 'https://hooks.slack.com/services/...'
                    : 'https://receiver.example.com/alerts'
                }
                value={draft.url}
                onChange={(event) => set({ url: event.target.value })}
              />
            )}
            {draft.kind === 'webhook' && (
              <Input.Password
                placeholder="Authorization header (optional)"
                value={draft.authorization}
                onChange={(event) => set({ authorization: event.target.value })}
              />
            )}
            {draft.kind === 'pagerduty' && (
              <Input.Password
                placeholder="Events API v2 routing key"
                value={draft.routingKey}
                onChange={(event) => set({ routingKey: event.target.value })}
              />
            )}
            {draft.kind === 'email' && (
              <Input
                placeholder="ops@example.com, oncall@example.com"
                value={draft.to}
                onChange={(event) => set({ to: event.target.value })}
              />
            )}
            <div className="text-xs font-medium text-gray-600 dark:text-gray-300">Available to</div>
            <Radio.Group
              value={draft.scopeMode}
              onChange={(event) => set({ scopeMode: event.target.value })}
            >
              <Radio value="all">
                {IS_CONTROL_PLANE ? 'Every project on every Site' : 'Every project'}
              </Radio>
              {IS_CONTROL_PLANE && <Radio value="sites">Every project on some Sites</Radio>}
              <Radio value="projects">Some projects</Radio>
            </Radio.Group>
            {draft.scopeMode === 'sites' && (
              <Select
                mode="multiple"
                placeholder="Sites"
                value={draft.sites}
                options={sites.map((site) => ({
                  value: site.id,
                  label: site.displayName || site.id,
                }))}
                onChange={(next: string[]) => set({ sites: next })}
              />
            )}
            {draft.scopeMode === 'projects' && (
              <Select
                mode="multiple"
                placeholder="Projects"
                value={draft.projects}
                options={projectOptions}
                onChange={(next: string[]) => set({ projects: next })}
              />
            )}
          </div>
        )}
      </Modal>
    </div>
  );
};

const scopeOf = (draft: ChannelDraft): ChannelScope => {
  switch (draft.scopeMode) {
    case 'all':
      return { allSites: true };
    case 'sites':
      return { allSites: false, sites: draft.sites };
    default:
      return {
        allSites: false,
        projects: draft.projects.map((value) => {
          const [siteId, projectId] = value.split('/');
          return { siteId, projectId };
        }),
      };
  }
};

const scopeText = (channel: SettingsChannel): string => {
  if (channel.scope) {
    if (channel.scope.allSites) return 'Every Site';
    const parts = [];
    if (channel.scope.sites?.length) parts.push(`Sites: ${channel.scope.sites.join(', ')}`);
    if (channel.scope.projects?.length) parts.push(`${channel.scope.projects.length} project(s)`);
    return parts.join('; ');
  }
  return channel.allProjects ? 'Every project' : `${channel.projectIds.length} project(s)`;
};

const Labelled = ({ label, children }: { label: string; children: React.ReactNode }) => (
  <div className="flex flex-col gap-1">
    <span className="text-xs font-medium text-gray-600 dark:text-gray-300">{label}</span>
    {children}
  </div>
);
