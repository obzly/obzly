// Site registry, control-plane mode only. Central ADMIN only.
import {
  CopyOutlined,
  DeleteOutlined,
  EditOutlined,
  LoadingOutlined,
  PlusOutlined,
  SafetyCertificateOutlined,
} from '@ant-design/icons';
import {
  App,
  Badge,
  Button,
  Drawer,
  Form,
  Input,
  Modal,
  Select,
  Spin,
  Switch,
  Table,
  Tag,
  Tooltip,
} from 'antd';
import type { ColumnsType } from 'antd/es/table';
import { useEffect, useState } from 'react';

import { APP_VERSION } from '../../../constants';
import {
  type SiteAdmin,
  type SiteEnvironment,
  type SiteHealth,
  type SiteStatus,
  sitesApi,
} from '../../../entity/sites';
import { copyToClipboard, useVisibleInterval } from '../../../shared/lib';
import {
  ENVIRONMENT_OPTIONS,
  ENVIRONMENT_STYLE,
  siteGroupKey,
  useSite,
} from '../../../shared/site';
import { SiteGrantsComponent } from './SiteGrantsComponent';

interface Props {
  /** Highlights a Site the Overview pointed at. */
  focusSiteId?: string;
}

interface SiteFormValues {
  id: string;
  name: string;
  apiUrl: string;
  enabled: boolean;
  displayName: string;
  environment: SiteEnvironment;
}

const MAX_DISPLAY_NAME_LENGTH = 64;

const STATUS_REFRESH_MS = 30_000;

const HEALTH_BADGE: Record<
  SiteHealth,
  { status: 'success' | 'warning' | 'error' | 'default'; label: string }
> = {
  up: { status: 'success', label: 'Up' },
  degraded: { status: 'warning', label: 'Degraded' },
  down: { status: 'error', label: 'Down' },
  disabled: { status: 'default', label: 'Disabled' },
};

function statusTooltip(status: SiteStatus): string {
  const parts = [
    status.message,
    status.version && `version ${status.version}`,
    status.latencyMs !== undefined && status.status !== 'disabled' && `${status.latencyMs} ms`,
    `checked ${new Date(status.checkedAt).toLocaleTimeString()}`,
  ];
  return parts.filter(Boolean).join(' · ');
}

export function SitesComponent({ focusSiteId }: Props) {
  const { message } = App.useApp();
  const { reloadSites } = useSite();

  const [sites, setSites] = useState<SiteAdmin[]>([]);
  const [isLoading, setIsLoading] = useState(true);
  const [editingSite, setEditingSite] = useState<SiteAdmin | 'new' | null>(null);
  const [grantsForSite, setGrantsForSite] = useState<SiteAdmin | null>(null);
  const [checkingSiteId, setCheckingSiteId] = useState<string | null>(null);
  const [statuses, setStatuses] = useState<Record<string, SiteStatus>>({});
  const [form] = Form.useForm<SiteFormValues>();

  const loadSites = async () => {
    setIsLoading(true);
    try {
      const result = await sitesApi.listSitesAdmin();
      setSites(result);
    } catch (e) {
      message.error((e as Error).message);
    }
    setIsLoading(false);
  };

  useEffect(() => {
    loadSites();
  }, []);

  // A failed refresh keeps the last known lights rather than toasting every 30s.
  const refreshStatuses = (isCancelled: () => boolean = () => false) =>
    sitesApi
      .getSiteStatuses()
      .then((result) => {
        if (!isCancelled()) setStatuses(Object.fromEntries(result.map((st) => [st.siteId, st])));
      })
      .catch(() => undefined);
  useEffect(() => {
    let cancelled = false;
    void refreshStatuses(() => cancelled);
    return () => {
      cancelled = true;
    };
  }, [sites]);
  useVisibleInterval(() => void refreshStatuses(), STATUS_REFRESH_MS);

  const openCreate = () => {
    form.resetFields();
    form.setFieldsValue({ enabled: true, displayName: '', environment: '' });
    setEditingSite('new');
  };

  const openEdit = (site: SiteAdmin) => {
    form.setFieldsValue(site);
    setEditingSite(site);
  };

  const handleSubmit = async () => {
    try {
      const values = await form.validateFields();
      const displayName = values.displayName?.trim() ?? '';
      const environment = values.environment ?? '';
      if (editingSite === 'new') {
        await sitesApi.createSite({
          id: values.id,
          name: values.name,
          apiUrl: values.apiUrl,
          displayName,
          environment,
        });
      } else if (editingSite) {
        await sitesApi.updateSite(editingSite.id, {
          name: values.name,
          apiUrl: values.apiUrl,
          enabled: values.enabled,
          displayName,
          environment,
        });
      }
      // The environment belongs to the Site name, so keep its other Site IDs in step.
      const siblings = sites.filter(
        (site) =>
          site.id !== values.id &&
          siteGroupKey(site) === siteGroupKey({ ...site, name: values.name }) &&
          site.environment !== environment,
      );
      await Promise.all(
        siblings.map((site) =>
          sitesApi.updateSite(site.id, {
            name: site.name,
            apiUrl: site.apiUrl,
            enabled: site.enabled,
            environment,
          }),
        ),
      );
      message.success(
        editingSite === 'new'
          ? 'Site registered'
          : siblings.length
            ? `Site updated, and the environment applied to ${siblings.length} other Site ID${siblings.length > 1 ? 's' : ''} named "${values.name.trim()}"`
            : 'Site updated',
      );
      setEditingSite(null);
      await loadSites();
      await reloadSites();
    } catch (e) {
      if (e instanceof Error) message.error(e.message);
    }
  };

  const handleDelete = async (site: SiteAdmin) => {
    Modal.confirm({
      title: `Remove "${site.name}"?`,
      content: 'Every grant for this Site will also be removed. This cannot be undone.',
      okButtonProps: { danger: true },
      onOk: async () => {
        try {
          await sitesApi.deleteSite(site.id);
          message.success('Site removed');
          await loadSites();
          await reloadSites();
        } catch (e) {
          message.error((e as Error).message);
        }
      },
    });
  };

  const handleCheck = async (site: SiteAdmin) => {
    setCheckingSiteId(site.id);
    try {
      const result = await sitesApi.checkSite(site.id);
      if (result.reachable && result.siteIdMatches) {
        message.success(
          `Reachable - reports itself as "${result.reportedSiteId}" (v${result.version})`,
        );
      } else if (result.reachable) {
        message.warning(
          `Reachable, but reports a different site id: "${result.reportedSiteId}" (expected "${site.id}")`,
        );
      } else {
        message.error(result.error || 'Site unreachable');
      }
    } catch (e) {
      message.error((e as Error).message);
    }
    setCheckingSiteId(null);
  };

  const columns: ColumnsType<SiteAdmin> = [
    {
      title: 'Status',
      key: 'health',
      width: 120,
      render: (_, site) => {
        const status = statuses[site.id];
        if (!status) return <Badge status="processing" text="Checking" />;
        const badge = HEALTH_BADGE[status.status];
        return (
          <Tooltip title={statusTooltip(status)}>
            <Badge status={badge.status} text={badge.label} />
          </Tooltip>
        );
      },
    },
    {
      // Amber when it differs from the control plane's build, to spot drift.
      title: (
        <Tooltip title={`Control plane: ${APP_VERSION}`}>
          <span>Version</span>
        </Tooltip>
      ),
      key: 'version',
      width: 130,
      render: (_, site) => {
        const version = statuses[site.id]?.version;
        if (!version) return <span className="text-gray-400">-</span>;
        if (version === APP_VERSION) return <Tag className="font-mono">{version}</Tag>;
        return (
          <Tooltip title={`Differs from the control plane (${APP_VERSION})`}>
            <Tag color="orange" className="font-mono">
              {version}
            </Tag>
          </Tooltip>
        );
      },
    },
    {
      // The Site's own SITE_ID, also its address here (/sites/<id>/...).
      title: 'Site ID',
      dataIndex: 'id',
      key: 'id',
      render: (_, site) => (
        <span className="inline-flex items-center gap-1">
          <span className="flex flex-col whitespace-nowrap">
            {site.displayName && (
              <span className="text-gray-900 dark:text-gray-100">{site.displayName}</span>
            )}
            <span
              className={`font-mono ${site.displayName ? 'text-xs text-gray-500 dark:text-gray-400' : ''}`}
            >
              {site.id}
            </span>
          </span>
          <Tooltip title="Copy Site ID">
            <Button
              type="text"
              size="small"
              icon={<CopyOutlined />}
              aria-label={`Copy Site ID ${site.id}`}
              onClick={async () => {
                await copyToClipboard(site.id);
                message.success('Site ID copied');
              }}
            />
          </Tooltip>
        </span>
      ),
    },
    {
      title: 'Name',
      key: 'name',
      render: (_, site) => (
        <span className="inline-flex items-center gap-2">
          {site.name}
          {site.environment && (
            <span
              className={`rounded px-1.5 py-0.5 text-xs ${ENVIRONMENT_STYLE[site.environment].badge}`}
            >
              {ENVIRONMENT_STYLE[site.environment].label}
            </span>
          )}
        </span>
      ),
    },
    { title: 'API URL', dataIndex: 'apiUrl', key: 'apiUrl' },
    {
      title: 'Enabled',
      key: 'enabled',
      render: (_, site) =>
        site.enabled ? <Tag color="green">Enabled</Tag> : <Tag color="default">Disabled</Tag>,
    },
    {
      title: 'Actions',
      key: 'actions',
      width: 220,
      render: (_, site) => (
        <div className="flex gap-1">
          <Button
            size="small"
            icon={
              checkingSiteId === site.id ? <LoadingOutlined spin /> : <SafetyCertificateOutlined />
            }
            onClick={() => handleCheck(site)}
            disabled={checkingSiteId === site.id}
          >
            Check
          </Button>
          <Button size="small" onClick={() => setGrantsForSite(site)}>
            Grants
          </Button>
          <Button size="small" icon={<EditOutlined />} onClick={() => openEdit(site)} />
          <Button
            size="small"
            danger
            icon={<DeleteOutlined />}
            onClick={() => handleDelete(site)}
          />
        </div>
      ),
    },
  ];

  return (
    <div>
      <div className="mb-4 flex items-center justify-between">
        <div className="text-sm text-gray-500 dark:text-gray-300">
          Every Site this control plane fronts, its address and who can open it.
        </div>
        <Button type="primary" icon={<PlusOutlined />} onClick={openCreate}>
          Register site
        </Button>
      </div>

      {isLoading ? (
        <div className="flex justify-center py-10">
          <Spin indicator={<LoadingOutlined spin />} size="large" />
        </div>
      ) : (
        <Table<SiteAdmin>
          rowKey="id"
          columns={columns}
          dataSource={sites}
          pagination={false}
          scroll={{ x: 900 }}
          rowClassName={(site) =>
            site.id === focusSiteId ? 'bg-amber-50 dark:bg-amber-900/20' : ''
          }
        />
      )}

      <Modal
        title={editingSite === 'new' ? 'Register a Site' : 'Edit Site'}
        open={editingSite !== null}
        onCancel={() => setEditingSite(null)}
        onOk={handleSubmit}
        okText="Save"
      >
        <Form form={form} layout="vertical">
          <Form.Item
            name="id"
            label="Site ID"
            rules={[
              {
                required: true,
                pattern: /^[a-z0-9][a-z0-9-]{0,62}$/,
                message: 'lowercase letters, digits and hyphens',
              },
            ]}
          >
            <Input disabled={editingSite !== 'new'} placeholder="eu-west-1" />
          </Form.Item>
          <Form.Item
            name="name"
            label="Name"
            rules={[{ required: true }]}
            extra="Sites with the same name appear as one entry in the site picker, with their projects listed together."
          >
            <Input placeholder="EU West" />
          </Form.Item>
          <Form.Item
            name="displayName"
            label="Friendly name (optional)"
            rules={[{ max: MAX_DISPLAY_NAME_LENGTH }]}
            extra="Shown in the site picker instead of the Site ID. Leave empty to show the Site ID."
          >
            <Input placeholder="US East" />
          </Form.Item>
          <Form.Item
            name="environment"
            label="Environment"
            extra="Applies to every Site ID with this name. Production shows a red warning in the site picker; not set stays neutral."
          >
            <Select options={ENVIRONMENT_OPTIONS} />
          </Form.Item>
          <Form.Item name="apiUrl" label="API URL" rules={[{ required: true }]}>
            <Input placeholder="https://eu-west-1.internal:4005" />
          </Form.Item>
          {editingSite !== 'new' && (
            <Form.Item name="enabled" label="Enabled" valuePropName="checked">
              <Switch />
            </Form.Item>
          )}
        </Form>
      </Modal>

      <Drawer
        title={
          grantsForSite ? (
            <span>
              Access - {grantsForSite.name}{' '}
              <span className="font-mono text-sm font-normal text-gray-400">
                ({grantsForSite.id})
              </span>
            </span>
          ) : (
            ''
          )
        }
        open={grantsForSite !== null}
        onClose={() => setGrantsForSite(null)}
        width={720}
      >
        {grantsForSite && <SiteGrantsComponent site={grantsForSite} />}
      </Drawer>
    </div>
  );
}
