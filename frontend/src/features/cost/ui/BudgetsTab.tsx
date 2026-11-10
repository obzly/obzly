import { DeleteOutlined, EditOutlined, LoadingOutlined, PlusOutlined } from '@ant-design/icons';
import {
  App,
  Button,
  Empty,
  Form,
  Input,
  InputNumber,
  Modal,
  Popconfirm,
  Progress,
  Segmented,
  Select,
  Spin,
  Table,
  Tooltip,
} from 'antd';
import dayjs from 'dayjs';
import { useCallback, useEffect, useState } from 'react';

import {
  type CostAnomaly,
  type CostBudget,
  type CostBudgetRequest,
  type FacetsResponse,
  type ProjectChannel,
  costApi,
} from '../../../entity/cost';
import { projectApi, splitSystemProject } from '../../../entity/projects';
import type { ProjectResponse } from '../../../entity/projects';
import { Panel } from '../../synthetics';
import { type BaseDimension, DIMENSION_LABEL, valueLabel } from '../lib/explorer';
import { formatMoney } from '../lib/format';
import { type CostSite, mergeFacets } from '../lib/merge';
import { addDays, resolveRange } from '../lib/ranges';
import { fanOut } from '../lib/sites';
import { SiteErrors } from './parts';

interface Props {
  sites: CostSite[];
  refresh: number;
}

type SiteAnomaly = CostAnomaly & CostSite;

const BUDGET_DIMENSIONS: BaseDimension[] = ['provider', 'account', 'region', 'service'];
const STATUS_COLOR: Record<CostBudget['status'], string> = {
  ok: '#009966',
  near: '#d97706',
  over: '#dc2626',
};
const today = () => new Date().toISOString().slice(0, 10);

export const BudgetsTab = ({ sites, refresh }: Props) => {
  const { message } = App.useApp();
  const [siteId, setSiteId] = useState(sites[0]?.siteId ?? '');
  const [projects, setProjects] = useState<ProjectResponse[]>();
  const [projectId, setProjectId] = useState<string>();
  const [budgets, setBudgets] = useState<CostBudget[]>();
  const [channels, setChannels] = useState<ProjectChannel[]>([]);
  const [anomalyChannels, setAnomalyChannels] = useState<string[]>([]);
  const [error, setError] = useState<string>();
  const [editing, setEditing] = useState<CostBudget | 'new'>();
  const [anomalies, setAnomalies] = useState<{
    list: SiteAnomaly[];
    failed: { site: CostSite; error?: string }[];
  }>();
  const [status, setStatus] = useState<'open' | 'all'>('open');
  const [ack, setAck] = useState<{ anomaly: SiteAnomaly; note: string }>();

  useEffect(() => {
    setProjects(undefined);
    setProjectId(undefined);
    (siteId ? projectApi.getProjectsOnSite(siteId) : projectApi.getProjects())
      .then((response) => {
        const regular = splitSystemProject(response.projects).regular;
        setProjects(regular);
        setProjectId(regular[0]?.id);
      })
      .catch((reason: Error) => {
        setProjects([]);
        setError(reason.message);
      });
  }, [siteId]);

  const loadBudgets = useCallback(() => {
    if (!projectId) return;
    setError(undefined);
    Promise.all([
      costApi.budgets(siteId, projectId),
      costApi.channels(siteId, projectId),
      costApi.alertSettings(siteId, projectId),
    ])
      .then(([list, channelList, settings]) => {
        setBudgets(list);
        setChannels(channelList);
        setAnomalyChannels(settings.anomalyChannelIds);
      })
      .catch((reason: Error) => {
        setBudgets([]);
        setError(reason.message);
      });
  }, [siteId, projectId]);

  useEffect(loadBudgets, [loadBudgets, refresh]);

  useEffect(() => {
    const day = today();
    void fanOut(sites, (id) => costApi.anomalies(id, addDays(day, -89), day)).then((outcomes) =>
      setAnomalies({
        list: outcomes
          .flatMap((outcome) =>
            (outcome.value ?? []).map((anomaly) => ({ ...anomaly, ...outcome.site })),
          )
          .sort((a, b) => b.day.localeCompare(a.day) || b.score - a.score),
        failed: outcomes,
      }),
    );
  }, [sites, refresh]);

  const saveAnomalyChannels = async (ids: string[]) => {
    if (!projectId) return;
    try {
      const saved = await costApi.updateAlertSettings(siteId, projectId, {
        anomalyChannelIds: ids,
      });
      setAnomalyChannels(saved.anomalyChannelIds);
      message.success(ids.length ? 'Anomaly alerts on' : 'Anomaly alerts off');
    } catch (reason) {
      message.error((reason as Error).message);
    }
  };

  const removeBudget = async (budget: CostBudget) => {
    try {
      await costApi.deleteBudget(siteId, budget.projectId, budget.id);
      message.success(`Deleted “${budget.name}”`);
      loadBudgets();
    } catch (reason) {
      message.error((reason as Error).message);
    }
  };

  const acknowledge = async () => {
    if (!ack) return;
    try {
      const updated = await costApi.updateAnomaly(
        ack.anomaly.siteId,
        ack.anomaly.id,
        'acknowledged',
        ack.note,
      );
      setAnomalies(
        (current) =>
          current && {
            ...current,
            list: current.list.map((item) =>
              item.id === updated.id && item.siteId === ack.anomaly.siteId
                ? { ...item, ...updated }
                : item,
            ),
          },
      );
      setAck(undefined);
      message.success('Anomaly acknowledged');
    } catch (reason) {
      message.error((reason as Error).message);
    }
  };

  const reopen = async (anomaly: SiteAnomaly) => {
    try {
      const updated = await costApi.updateAnomaly(anomaly.siteId, anomaly.id, 'open', anomaly.note);
      setAnomalies(
        (current) =>
          current && {
            ...current,
            list: current.list.map((item) =>
              item.id === updated.id && item.siteId === anomaly.siteId
                ? { ...item, ...updated }
                : item,
            ),
          },
      );
      message.success('Anomaly reopened');
    } catch (reason) {
      message.error((reason as Error).message);
    }
  };

  const channelOptions = channels.map((channel) => ({
    value: channel.id,
    label: `${channel.name} (${channel.kind})`,
  }));
  const shownAnomalies = (anomalies?.list ?? []).filter(
    (anomaly) => status === 'all' || anomaly.status === 'open',
  );

  return (
    <div className="flex flex-col gap-4">
      <div className="flex flex-wrap items-center gap-2 text-sm text-gray-600 dark:text-gray-300">
        {sites.length > 1 && (
          <>
            Site
            <Select
              size="small"
              className="w-44"
              value={siteId}
              onChange={setSiteId}
              options={sites.map((site) => ({ value: site.siteId, label: site.siteName }))}
            />
          </>
        )}
        Project
        <Select
          size="small"
          className="w-56"
          loading={!projects}
          value={projectId}
          onChange={setProjectId}
          options={(projects ?? []).map((project) => ({ value: project.id, label: project.name }))}
          notFoundContent="No project you can open here"
        />
        <span className="text-xs text-gray-500 dark:text-gray-400">
          Budgets and alerts belong to the project the connection delivers cost to.
        </span>
      </div>

      {error && (
        <div className="rounded border border-red-300 bg-red-50 px-3 py-2 text-sm text-red-700 dark:border-red-800 dark:bg-red-950 dark:text-red-300">
          {error}
        </div>
      )}

      <Panel
        title="Budgets"
        subtitle="This month's effective cost against each budget; alerts go out when the projection crosses the threshold"
        extra={
          <Button
            size="small"
            type="primary"
            icon={<PlusOutlined />}
            disabled={!projectId}
            onClick={() => setEditing('new')}
          >
            New budget
          </Button>
        }
      >
        {!budgets && projectId ? (
          <div className="flex justify-center py-8">
            <Spin indicator={<LoadingOutlined spin />} />
          </div>
        ) : (budgets ?? []).length === 0 ? (
          <Empty className="py-6" description="No budgets yet" />
        ) : (
          <Table<CostBudget>
            size="small"
            rowKey="id"
            pagination={false}
            dataSource={budgets}
            scroll={{ x: 'max-content' }}
            columns={[
              { title: 'Name', dataIndex: 'name' },
              {
                title: 'Scope',
                render: (_, budget) => budgetScope(budget) || 'All cost of the project',
              },
              {
                title: 'Spent',
                render: (_, budget) => (
                  <div className="w-48">
                    <Progress
                      size="small"
                      percent={Math.min(100, Math.round((budget.spent / budget.amount) * 100))}
                      strokeColor={STATUS_COLOR[budget.status]}
                      format={() => formatMoney(budget.spent, budget.currency)}
                    />
                  </div>
                ),
              },
              {
                title: 'Projected',
                render: (_, budget) => (
                  <Tooltip
                    title={
                      budget.method === 'forecast'
                        ? 'The providers’ forecast for the month'
                        : 'This month so far plus the last days’ average for the rest'
                    }
                  >
                    <span style={{ color: STATUS_COLOR[budget.status] }} className="tabular-nums">
                      {formatMoney(budget.projected, budget.currency)} ({Math.round(budget.percent)}
                      %)
                    </span>
                  </Tooltip>
                ),
              },
              {
                title: 'Budget',
                render: (_, budget) => (
                  <span className="tabular-nums">
                    {formatMoney(budget.amount, budget.currency)}, alert at{' '}
                    {budget.thresholdPercent}%
                  </span>
                ),
              },
              {
                title: 'Alerts to',
                render: (_, budget) =>
                  budget.channelIds.length ? (
                    budget.channelIds
                      .map(
                        (id) =>
                          channels.find((channel) => channel.id === id)?.name ?? 'removed channel',
                      )
                      .join(', ')
                  ) : (
                    <span className="text-gray-400">No channel</span>
                  ),
              },
              {
                key: 'actions',
                align: 'right',
                render: (_, budget) => (
                  <span className="flex justify-end gap-1">
                    <Tooltip title="Edit">
                      <Button
                        size="small"
                        type="text"
                        aria-label="Edit"
                        icon={<EditOutlined />}
                        onClick={() => setEditing(budget)}
                      />
                    </Tooltip>
                    <Popconfirm
                      title={`Delete “${budget.name}”?`}
                      description="Its alert rule is removed too."
                      okText="Delete"
                      okButtonProps={{ danger: true }}
                      onConfirm={() => removeBudget(budget)}
                    >
                      <Button
                        size="small"
                        type="text"
                        danger
                        aria-label="Delete"
                        icon={<DeleteOutlined />}
                      />
                    </Popconfirm>
                  </span>
                ),
              },
            ]}
          />
        )}
      </Panel>

      <Panel
        title="Anomalies"
        subtitle="A day far from the same weekday's last 8 weeks, per account and service; last 90 days"
        extra={
          <div className="flex flex-wrap items-center gap-2">
            <Tooltip title="Channels of the selected project that get new anomalies">
              <span className="text-xs text-gray-500 dark:text-gray-400">Alert</span>
            </Tooltip>
            <Select
              size="small"
              mode="multiple"
              className="min-w-56"
              placeholder="No channel (off)"
              disabled={!projectId}
              value={anomalyChannels}
              onChange={saveAnomalyChannels}
              options={channelOptions}
              notFoundContent="The project has no notification channels"
            />
            <Segmented
              size="small"
              value={status}
              onChange={(value) => setStatus(value as 'open' | 'all')}
              options={[
                { value: 'open', label: 'Open' },
                { value: 'all', label: 'All' },
              ]}
            />
          </div>
        }
      >
        {anomalies && <SiteErrors outcomes={anomalies.failed} />}
        <Table<SiteAnomaly>
          size="small"
          loading={!anomalies}
          rowKey={(anomaly) => `${anomaly.siteId}/${anomaly.id}`}
          dataSource={shownAnomalies}
          pagination={shownAnomalies.length > 20 ? { pageSize: 20, showSizeChanger: false } : false}
          scroll={{ x: 'max-content' }}
          locale={{ emptyText: status === 'open' ? 'No open anomalies' : 'No anomalies' }}
          columns={[
            { title: 'Day', dataIndex: 'day' },
            {
              title: 'Where',
              render: (_, anomaly) => (
                <div>
                  <div>{anomaly.service || 'Whole account'}</div>
                  <div className="text-xs text-gray-500 dark:text-gray-400">
                    {valueLabel('account', anomaly.subAccount, anomaly.subAccountName)} ·{' '}
                    {anomaly.connectionName}
                    {sites.length > 1 && ` · ${anomaly.siteName}`}
                  </div>
                </div>
              ),
            },
            {
              title: 'Expected',
              align: 'right',
              render: (_, anomaly) => (
                <span className="tabular-nums">
                  {formatMoney(anomaly.expected, anomaly.currency)}
                </span>
              ),
            },
            {
              title: 'Actual',
              align: 'right',
              render: (_, anomaly) => (
                <Tooltip
                  title={`Why flagged: ${anomaly.score.toFixed(1)} robust deviations from the median of the same weekday over 8 weeks, and the change is large enough to matter`}
                >
                  <span className="font-semibold text-red-600 tabular-nums dark:text-red-400">
                    {formatMoney(anomaly.actual, anomaly.currency)}
                  </span>
                </Tooltip>
              ),
            },
            {
              title: 'Status',
              render: (_, anomaly) =>
                anomaly.status === 'open' ? (
                  'Open'
                ) : (
                  <Tooltip title={anomaly.note || undefined}>
                    <span className="text-gray-500 dark:text-gray-400">
                      Acknowledged{anomaly.note ? ' (note)' : ''}
                    </span>
                  </Tooltip>
                ),
            },
            {
              title: 'Detected',
              render: (_, anomaly) => dayjs(anomaly.detectedAt).fromNow(),
            },
            {
              key: 'actions',
              align: 'right',
              render: (_, anomaly) =>
                anomaly.status === 'open' ? (
                  <Button size="small" onClick={() => setAck({ anomaly, note: anomaly.note })}>
                    Acknowledge
                  </Button>
                ) : (
                  <Button size="small" type="link" onClick={() => reopen(anomaly)}>
                    Reopen
                  </Button>
                ),
            },
          ]}
        />
      </Panel>

      <Modal
        open={!!ack}
        title="Acknowledge anomaly"
        okText="Acknowledge"
        onOk={acknowledge}
        onCancel={() => setAck(undefined)}
      >
        <Input.TextArea
          rows={3}
          maxLength={1000}
          placeholder="Optional note, e.g. planned load test"
          value={ack?.note}
          onChange={(event) =>
            setAck((current) => current && { ...current, note: event.target.value })
          }
        />
      </Modal>

      {editing && projectId && (
        <BudgetEditor
          siteId={siteId}
          projectId={projectId}
          budget={editing === 'new' ? undefined : editing}
          channelOptions={channelOptions}
          onClose={() => setEditing(undefined)}
          onSaved={() => {
            setEditing(undefined);
            loadBudgets();
          }}
        />
      )}
    </div>
  );
};

const budgetScope = (budget: CostBudget): string =>
  BUDGET_DIMENSIONS.filter((dimension) => budget.filters[dimension]?.length)
    .map(
      (dimension) =>
        `${DIMENSION_LABEL[dimension]}: ${budget.filters[dimension]!.map((value) => valueLabel(dimension, value)).join(', ')}`,
    )
    .join(' · ');

interface EditorProps {
  siteId: string;
  projectId: string;
  budget?: CostBudget;
  channelOptions: { value: string; label: string }[];
  onClose: () => void;
  onSaved: () => void;
}

const BudgetEditor = ({
  siteId,
  projectId,
  budget,
  channelOptions,
  onClose,
  onSaved,
}: EditorProps) => {
  const { message } = App.useApp();
  const [form] = Form.useForm<CostBudgetRequest>();
  const [facets, setFacets] = useState<FacetsResponse>({ dimensions: {} });
  const [saving, setSaving] = useState(false);

  useEffect(() => {
    void fanOut([{ siteId, siteName: '' }], (id) =>
      costApi.facets(id, {
        ...resolveRange('last90', today()),
        granularity: 'monthly',
        groupBy: 'service',
        metric: 'effective',
        filters: {},
        projectId,
      }),
    ).then((outcomes) => setFacets(mergeFacets(outcomes)));
  }, [siteId, projectId]);

  const save = async () => {
    const values = await form.validateFields();
    const filters = Object.fromEntries(
      BUDGET_DIMENSIONS.map((dimension) => [dimension, values.filters?.[dimension] ?? []]).filter(
        ([, list]) => (list as string[]).length > 0,
      ),
    );
    setSaving(true);
    try {
      const request = { ...values, filters, channelIds: values.channelIds ?? [] };
      if (budget) await costApi.updateBudget(siteId, projectId, budget.id, request);
      else await costApi.createBudget(siteId, projectId, request);
      message.success(budget ? 'Budget saved' : 'Budget created');
      onSaved();
    } catch (reason) {
      message.error((reason as Error).message);
    } finally {
      setSaving(false);
    }
  };

  return (
    <Modal
      open
      title={budget ? `Edit “${budget.name}”` : 'New budget'}
      okText="Save"
      confirmLoading={saving}
      onOk={save}
      onCancel={onClose}
    >
      <Form<CostBudgetRequest>
        form={form}
        layout="vertical"
        initialValues={
          budget ?? { currency: 'USD', thresholdPercent: 80, filters: {}, channelIds: [] }
        }
      >
        <Form.Item name="name" label="Name" rules={[{ required: true, max: 100 }]}>
          <Input placeholder="e.g. Production AWS" />
        </Form.Item>
        <div className="grid grid-cols-2 gap-3">
          <Form.Item
            name="amount"
            label="Monthly amount"
            rules={[{ required: true, type: 'number', min: 0.01 }]}
          >
            <InputNumber className="w-full" min={0.01} />
          </Form.Item>
          <Form.Item
            name="currency"
            label="Currency"
            rules={[
              { required: true, pattern: /^[A-Za-z]{3}$/, message: 'A three-letter code like USD' },
            ]}
            tooltip="Only cost billed in this currency counts"
          >
            <Input maxLength={3} />
          </Form.Item>
        </div>
        <Form.Item
          name="thresholdPercent"
          label="Alert when the month is projected to reach (%)"
          rules={[{ required: true, type: 'number', min: 1, max: 100 }]}
        >
          <InputNumber className="w-full" min={1} max={100} />
        </Form.Item>
        {BUDGET_DIMENSIONS.map((dimension) => (
          <Form.Item
            key={dimension}
            name={['filters', dimension]}
            label={DIMENSION_LABEL[dimension]}
          >
            <Select
              mode="multiple"
              allowClear
              placeholder="Any"
              options={(facets.dimensions[dimension] ?? []).map((value) => ({
                value: value.value,
                label: valueLabel(dimension, value.value, value.label),
              }))}
            />
          </Form.Item>
        ))}
        <Form.Item name="channelIds" label="Alert channels">
          <Select
            mode="multiple"
            allowClear
            placeholder="None: the budget is shown but sends no alert"
            options={channelOptions}
            notFoundContent="The project has no notification channels"
          />
        </Form.Item>
      </Form>
    </Modal>
  );
};
