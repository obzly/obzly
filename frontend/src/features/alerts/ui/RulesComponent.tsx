import { LoadingOutlined, PlusOutlined } from '@ant-design/icons';
import { App, Button, Popconfirm, Spin, Switch, Table, Tag, Tooltip } from 'antd';
import dayjs from 'dayjs';
import { useEffect, useState } from 'react';

import { type AlertRule, type NotificationChannel, alertingApi } from '../../../entity/alerting';
import { RuleEditorDrawerComponent } from './RuleEditorDrawerComponent';
import { errorMessage, toRequest } from './lib';

interface Props {
  projectId: string;
  canEdit: boolean;
}

// Budget rules have no query to view or edit here; their budget is the editor.
const isBudgetRule = (rule: AlertRule) => rule.managedBy?.startsWith('ai-budget:') ?? false;

export const RulesComponent = ({ projectId, canEdit }: Props) => {
  const { message } = App.useApp();
  const [rules, setRules] = useState<AlertRule[]>([]);
  const [channels, setChannels] = useState<NotificationChannel[]>([]);
  const [isLoading, setIsLoading] = useState(true);
  const [editing, setEditing] = useState<AlertRule | 'new' | null>(null);

  const load = async () => {
    setIsLoading(true);
    try {
      const [loadedRules, loadedChannels] = await Promise.all([
        alertingApi.listRules(projectId),
        alertingApi.listChannels(projectId),
      ]);
      setRules(loadedRules);
      setChannels(loadedChannels);
    } catch (error: unknown) {
      message.error(errorMessage(error, 'Failed to load rules'));
    } finally {
      setIsLoading(false);
    }
  };

  const toggle = async (rule: AlertRule, enabled: boolean) => {
    try {
      const updated = await alertingApi.updateRule(projectId, rule.id, {
        ...toRequest(rule),
        enabled,
      });
      setRules((prev) => prev.map((r) => (r.id === rule.id ? updated : r)));
      message.success(enabled ? 'Rule enabled' : 'Rule paused');
    } catch (error: unknown) {
      message.error(errorMessage(error, 'Failed to update rule'));
    }
  };

  const remove = async (rule: AlertRule) => {
    try {
      await alertingApi.deleteRule(projectId, rule.id);
      setRules((prev) => prev.filter((r) => r.id !== rule.id));
      message.success('Rule deleted');
    } catch (error: unknown) {
      message.error(errorMessage(error, 'Failed to delete rule'));
    }
  };

  useEffect(() => {
    load();
  }, [projectId]);

  if (isLoading) {
    return <Spin indicator={<LoadingOutlined spin />} />;
  }

  const channelName = (id: string) => channels.find((c) => c.id === id)?.name ?? 'deleted';

  return (
    <div>
      {canEdit && (
        <div className="mb-3 flex justify-end">
          <Button
            type="primary"
            icon={<PlusOutlined />}
            onClick={() => setEditing('new')}
            className="border-emerald-600 bg-emerald-600 hover:border-emerald-700 hover:bg-emerald-700"
          >
            New rule
          </Button>
        </div>
      )}
      <Table
        rowKey="id"
        dataSource={rules}
        pagination={false}
        locale={{ emptyText: 'No alert rules yet' }}
        columns={[
          {
            title: 'On',
            width: 70,
            render: (_, rule) => (
              <Switch
                size="small"
                checked={rule.enabled}
                disabled={!canEdit || !!rule.managedBy}
                onChange={(enabled) => toggle(rule, enabled)}
              />
            ),
          },
          {
            title: 'Rule',
            render: (_, rule) => (
              <div>
                <div className="font-medium text-gray-900 dark:text-gray-100">
                  {rule.name}
                  {rule.managedBy && isBudgetRule(rule) && (
                    <Tooltip title="Kept by an AI budget; change it in AI Observability, Budgets.">
                      <Tag className="ml-2">Budget</Tag>
                    </Tooltip>
                  )}
                  {rule.managedBy && !isBudgetRule(rule) && (
                    <Tooltip title="Kept by a Synthetics monitor's alerts in the control plane; change it there.">
                      <Tag className="ml-2">Synthetics</Tag>
                    </Tooltip>
                  )}
                </div>
                <div className="text-xs text-gray-500 dark:text-gray-300">
                  {isBudgetRule(rule) ? (
                    rule.summary
                  ) : (
                    <>
                      {rule.source} {rule.comparison} {rule.threshold}
                      {rule.forSeconds > 0 && ` for ${rule.forSeconds}s`}
                    </>
                  )}
                </div>
              </div>
            ),
          },
          {
            title: 'Channels',
            width: 220,
            render: (_, rule) =>
              rule.channelIds.length === 0 ? (
                <span className="text-xs text-gray-400">none</span>
              ) : (
                rule.channelIds.map((id) => <Tag key={id}>{channelName(id)}</Tag>)
              ),
          },
          {
            title: 'Last evaluated',
            width: 170,
            render: (_, rule) =>
              rule.lastError ? (
                <Tooltip title={rule.lastError}>
                  <Tag color="red">error</Tag>
                </Tooltip>
              ) : rule.lastEvalAt ? (
                dayjs(rule.lastEvalAt).fromNow()
              ) : (
                <span className="text-xs text-gray-400">not yet</span>
              ),
          },
          {
            title: '',
            width: 160,
            render: (_, rule) => (
              <div className="flex gap-2">
                {!isBudgetRule(rule) && (
                  <Button size="small" onClick={() => setEditing(rule)}>
                    {canEdit && !rule.managedBy ? 'Edit' : 'View'}
                  </Button>
                )}
                {canEdit && !rule.managedBy && (
                  <Popconfirm
                    title={`Delete ${rule.name}?`}
                    okText="Delete"
                    okButtonProps={{ danger: true }}
                    onConfirm={() => remove(rule)}
                  >
                    <Button size="small" danger>
                      Delete
                    </Button>
                  </Popconfirm>
                )}
              </div>
            ),
          },
        ]}
      />
      {editing && (
        <RuleEditorDrawerComponent
          projectId={projectId}
          rule={editing === 'new' ? null : editing}
          channels={channels}
          canEdit={canEdit && !(editing !== 'new' && editing.managedBy)}
          onClose={() => setEditing(null)}
          onSaved={(saved) => {
            setRules((prev) =>
              prev.some((r) => r.id === saved.id)
                ? prev.map((r) => (r.id === saved.id ? saved : r))
                : [...prev, saved],
            );
            setEditing(null);
          }}
        />
      )}
    </div>
  );
};
