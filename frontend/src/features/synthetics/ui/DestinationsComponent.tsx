import { DeleteOutlined, EditOutlined, PlusOutlined } from '@ant-design/icons';
import { App, Button, Drawer, Input, Popconfirm, Radio, Select, Table } from 'antd';
import { useEffect, useState } from 'react';

import { projectApi } from '../../../entity/projects/api/projectApi';
import type { ProjectResponse } from '../../../entity/projects/model/ProjectResponse';
import {
  type Destination,
  type DestinationRequest,
  syntheticsApi,
} from '../../../entity/synthetics';
import { useSite } from '../../../shared/site';
import { IconAction } from './parts';

interface Props {
  destinations: Destination[];
  onChanged: () => void;
}

type KeySource = 'create' | 'paste';

const empty: DestinationRequest = { name: '', siteId: '', projectId: '', otlpUrl: '', apiKey: '' };

export const DestinationsComponent = ({ destinations, onChanged }: Props) => {
  const { message } = App.useApp();
  const { sites } = useSite();
  const [editing, setEditing] = useState<Destination | 'new'>();
  const [form, setForm] = useState<DestinationRequest>(empty);
  const [keySource, setKeySource] = useState<KeySource>('create');
  const [projects, setProjects] = useState<ProjectResponse[]>([]);
  const [saving, setSaving] = useState(false);

  useEffect(() => {
    if (!form.siteId) {
      setProjects([]);
      return;
    }
    projectApi
      .getProjectsOnSite(form.siteId)
      .then((response) => setProjects(response.projects))
      .catch(() => setProjects([]));
  }, [form.siteId]);

  const open = (destination?: Destination) => {
    setEditing(destination ?? 'new');
    setKeySource(destination ? 'paste' : 'create');
    setForm(
      destination
        ? {
            name: destination.name,
            siteId: destination.siteId,
            projectId: destination.projectId,
            otlpUrl: destination.otlpUrl,
            apiKey: '',
          }
        : empty,
    );
  };

  const save = async () => {
    setSaving(true);
    try {
      let request = form;
      if (keySource === 'create' && (editing === 'new' || form.apiKey === '')) {
        // A key minted on the Site through the proxy, for the probes only.
        const key = await syntheticsApi.createSiteApiKey(
          form.siteId,
          form.projectId,
          `synthetics probes (${form.name})`,
        );
        request = { ...form, apiKey: key };
      }
      if (editing === 'new') {
        await syntheticsApi.createDestination(request);
      } else if (editing) {
        await syntheticsApi.updateDestination(editing.id, request);
      }
      message.success(`Destination ${form.name} saved`);
      setEditing(undefined);
      onChanged();
    } catch (reason) {
      message.error((reason as Error).message);
    } finally {
      setSaving(false);
    }
  };

  const remove = async (destination: Destination) => {
    try {
      await syntheticsApi.deleteDestination(destination.id);
      message.success(`Destination ${destination.name} deleted`);
      onChanged();
    } catch (reason) {
      message.error((reason as Error).message);
    }
  };

  return (
    <div>
      <div className="mb-3 flex items-center justify-between gap-3">
        <p className="m-0 text-sm text-gray-500 dark:text-gray-400">
          A destination is a Site project that stores results. Probes send to it directly with its
          API key.
        </p>
        <Button type="primary" icon={<PlusOutlined />} onClick={() => open()}>
          Add destination
        </Button>
      </div>
      <Table<Destination>
        rowKey="id"
        size="middle"
        dataSource={destinations}
        pagination={false}
        locale={{ emptyText: 'No destinations yet. Add one before creating monitors.' }}
        columns={[
          { title: 'Name', dataIndex: 'name' },
          {
            title: 'Site',
            dataIndex: 'siteId',
            render: (value: string) => <span className="font-mono text-xs">{value || '—'}</span>,
          },
          {
            title: 'Project',
            dataIndex: 'projectId',
            render: (value: string) => <span className="font-mono text-xs">{value || '—'}</span>,
          },
          {
            title: 'OTLP URL',
            dataIndex: 'otlpUrl',
            render: (value: string) => <span className="font-mono text-xs">{value}</span>,
          },
          {
            title: 'Key',
            dataIndex: 'apiKeyHint',
            render: (value: string) => <span className="font-mono text-xs">{value}</span>,
          },
          {
            title: '',
            key: 'actions',
            width: 90,
            render: (_, destination) => (
              <span className="inline-flex gap-0.5">
                <IconAction
                  title="Edit"
                  icon={<EditOutlined />}
                  onClick={() => open(destination)}
                />
                <Popconfirm
                  title={`Delete destination ${destination.name}?`}
                  okText="Delete"
                  okButtonProps={{ danger: true }}
                  onConfirm={() => remove(destination)}
                >
                  <span>
                    <IconAction title="Delete" icon={<DeleteOutlined />} danger />
                  </span>
                </Popconfirm>
              </span>
            ),
          },
        ]}
      />

      <Drawer
        open={!!editing}
        onClose={() => setEditing(undefined)}
        size={520}
        title={editing === 'new' ? 'Add destination' : 'Edit destination'}
        extra={
          <Button
            type="primary"
            loading={saving}
            onClick={save}
            disabled={
              !form.name ||
              !form.otlpUrl ||
              (keySource === 'create'
                ? !form.siteId || !form.projectId
                : editing === 'new' && !form.apiKey)
            }
          >
            Save
          </Button>
        }
      >
        <div className="flex flex-col gap-4">
          <Labelled label="Name" hint="Monitors and YAML files refer to it by this name.">
            <Input
              value={form.name}
              placeholder="synthetics"
              onChange={(event) => setForm({ ...form, name: event.target.value })}
            />
          </Labelled>
          <Labelled label="Site">
            <Select
              value={form.siteId || undefined}
              placeholder="The Site that stores the results"
              options={sites.map((site) => ({
                value: site.id,
                label: site.displayName || site.name || site.id,
              }))}
              onChange={(siteId) => setForm({ ...form, siteId, projectId: '' })}
            />
          </Labelled>
          <Labelled
            label="Project"
            hint="The UI reads results from here. A project of its own (e.g. Synthetics) keeps them apart."
          >
            <Select
              value={form.projectId || undefined}
              placeholder="Choose a project"
              options={projects.map((project) => ({ value: project.id, label: project.name }))}
              onChange={(projectId) => setForm({ ...form, projectId })}
            />
          </Labelled>
          <Labelled
            label="OTLP URL"
            hint="The Site's address as the probes reach it; /v1/metrics and /v1/logs are added."
          >
            <Input
              className="font-mono"
              value={form.otlpUrl}
              placeholder="https://obzly-site.example.com"
              onChange={(event) => setForm({ ...form, otlpUrl: event.target.value })}
            />
          </Labelled>
          <Labelled label="Ingest key">
            <Radio.Group value={keySource} onChange={(event) => setKeySource(event.target.value)}>
              <Radio value="create">Create a key on the Site for the probes</Radio>
              <Radio value="paste">Paste an existing key</Radio>
            </Radio.Group>
            {keySource === 'paste' && (
              <Input.Password
                className="mt-2"
                autoComplete="new-password"
                value={form.apiKey}
                placeholder={
                  editing !== 'new' && editing
                    ? `stored (${editing.apiKeyHint}); type to replace`
                    : 'lb_...'
                }
                onChange={(event) => setForm({ ...form, apiKey: event.target.value })}
              />
            )}
          </Labelled>
        </div>
      </Drawer>
    </div>
  );
};

const Labelled = ({
  label,
  hint,
  children,
}: {
  label: string;
  hint?: string;
  children: React.ReactNode;
}) => (
  <div className="flex flex-col gap-1">
    <span className="text-xs font-medium text-gray-600 dark:text-gray-300">{label}</span>
    {children}
    {hint && <span className="text-[11px] text-gray-500 dark:text-gray-400">{hint}</span>}
  </div>
);
