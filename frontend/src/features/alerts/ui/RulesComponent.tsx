import { LoadingOutlined, PlusOutlined } from '@ant-design/icons';
import { App, Button, Popconfirm, Spin, Switch, Table, Tooltip } from 'antd';
import dayjs from 'dayjs';
import { useEffect, useState } from 'react';

import {
  type AlertRule,
  type NotificationChannel,
  type RuleActivityResponse,
  type RuleRequest,
  alertingApi,
} from '../../../entity/alerting';
import { Card, RankedList } from '../../../shared/ui/v2';
import { noisiestRules, ruleDays } from '../lib/activity';
import { FiredBarsComponent } from './FiredBarsComponent';
import { RuleEditorDrawerComponent } from './RuleEditorDrawerComponent';
import { SeverityPill } from './SeverityPill';
import { Chip, Pill } from './bits';
import { errorMessage, managedRuleInfo, ruleSqlSummary, toRequest } from './lib';

interface Props {
  projectId: string;
  canEdit: boolean;
  /** After a rule is created, changed or deleted (the page header counts rules). */
  onChanged?: () => void;
  /** Opens the editor on a new rule with these values (Logs › Chart › Create alert). */
  initialDraft?: Partial<RuleRequest> | null;
}

// Under this many notifications per firing a rule is calm.
const NOISY_PER_FIRING = 5;

// Budget rules have no query to view or edit here; their budget is the editor.
const isBudgetRule = (rule: AlertRule) => rule.managedBy?.startsWith('ai-budget:') ?? false;

export const RulesComponent = ({ projectId, canEdit, onChanged, initialDraft }: Props) => {
  const { message } = App.useApp();
  const [rules, setRules] = useState<AlertRule[]>([]);
  const [channels, setChannels] = useState<NotificationChannel[]>([]);
  const [isLoading, setIsLoading] = useState(true);
  const [editing, setEditing] = useState<AlertRule | 'new' | null>(
    initialDraft && canEdit ? 'new' : null,
  );
  // Only the first new rule opened takes the draft; "New rule" later starts empty.
  const [draft, setDraft] = useState(initialDraft && canEdit ? initialDraft : null);
  // null: not loaded (failed); the table and the Noisiest card then say "n/a".
  const [activity, setActivity] = useState<RuleActivityResponse | null>(null);

  useEffect(() => {
    if (initialDraft && !canEdit) {
      message.info('Only project owners and admins can create alert rules');
    }
    // Only on the first render, for the draft the page opened with.
  }, []);

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

  // Separate from load, so the rules show even when the counts fail.
  const loadActivity = async () => {
    try {
      setActivity(await alertingApi.getRuleActivity(projectId));
    } catch {
      setActivity(null);
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
      onChanged?.();
    } catch (error: unknown) {
      message.error(errorMessage(error, 'Failed to update rule'));
    }
  };

  const remove = async (rule: AlertRule) => {
    try {
      await alertingApi.deleteRule(projectId, rule.id);
      setRules((prev) => prev.filter((r) => r.id !== rule.id));
      message.success('Rule deleted');
      onChanged?.();
    } catch (error: unknown) {
      message.error(errorMessage(error, 'Failed to delete rule'));
    }
  };

  useEffect(() => {
    load();
    loadActivity();
  }, [projectId]);

  if (isLoading) {
    return <Spin indicator={<LoadingOutlined spin />} />;
  }

  const channelName = (id: string) => channels.find((c) => c.id === id)?.name;
  const firedScale = activity ? Math.max(1, ...activity.rules.flatMap((r) => r.fired)) : 1;
  const noisy = activity ? noisiestRules(activity, rules) : [];
  const countingSince =
    activity?.trackedSince &&
    activity.days.length > 0 &&
    activity.trackedSince.slice(0, 10) > activity.days[0]
      ? dayjs(activity.trackedSince).format('MMM D')
      : null;

  return (
    <div className="grid grid-cols-1 gap-4 xl:grid-cols-2">
      <Card
        className="xl:col-span-2"
        title="Rules"
        subtitle="Checked on the Site; a paused rule keeps its history"
        extra={
          canEdit && (
            <Button
              type="primary"
              icon={<PlusOutlined />}
              onClick={() => setEditing('new')}
              className="border-accent-600 bg-accent-600 hover:border-accent-700 hover:bg-accent-700"
            >
              New rule
            </Button>
          )
        }
      >
        <Table
          rowKey="id"
          dataSource={rules}
          pagination={false}
          scroll={{ x: 'max-content' }}
          locale={{ emptyText: 'No alert rules yet' }}
          columns={[
            {
              title: 'On',
              width: 70,
              render: (_, rule) => {
                const toggleSwitch = (
                  <Switch
                    size="small"
                    checked={rule.enabled}
                    disabled={!canEdit || !!rule.managedBy}
                    aria-label={`${rule.enabled ? 'Pause' : 'Enable'} ${rule.name}`}
                    onChange={(enabled) => toggle(rule, enabled)}
                  />
                );
                // A disabled switch fires no mouse events, so the tooltip sits on a wrapper.
                return rule.managedBy ? (
                  <Tooltip title={managedRuleInfo(rule)?.tip}>
                    <span className="inline-flex">{toggleSwitch}</span>
                  </Tooltip>
                ) : (
                  toggleSwitch
                );
              },
            },
            {
              title: 'Rule',
              render: (_, rule) => {
                const managed = managedRuleInfo(rule);
                return (
                  <div className="flex flex-col gap-0.5">
                    <div className="flex flex-wrap items-center gap-1.5 font-medium text-gray-900 dark:text-gray-50">
                      {rule.name}
                      {managed && (
                        <Tooltip title={managed.tip}>
                          <Pill tone={managed.tone} tabIndex={0}>
                            {managed.label}
                          </Pill>
                        </Tooltip>
                      )}
                    </div>
                    <div className="font-mono text-[11.5px] text-gray-500 dark:text-gray-400">
                      {isBudgetRule(rule) ? (
                        rule.summary
                      ) : rule.source === 'logs_sql' ? (
                        <span title={rule.query.sql}>
                          {ruleSqlSummary(rule.query.sql ?? '')} {rule.comparison} {rule.threshold}
                          {rule.forSeconds > 0 && ` for ${rule.forSeconds}s`}
                        </span>
                      ) : (
                        <>
                          {rule.source} {rule.comparison} {rule.threshold}
                          {rule.forSeconds > 0 && ` for ${rule.forSeconds}s`}
                        </>
                      )}
                    </div>
                  </div>
                );
              },
            },
            {
              title: 'Severity',
              width: 100,
              render: (_, rule) => <SeverityPill severity={rule.severity} />,
            },
            {
              title: 'Channels',
              width: 220,
              render: (_, rule) =>
                rule.channelIds.length === 0 ? (
                  <span className="text-xs text-gray-500 dark:text-gray-400">none</span>
                ) : (
                  <span className="flex flex-wrap gap-1">
                    {rule.channelIds.map((id) => {
                      const name = channelName(id);
                      return (
                        <Chip key={id} danger={!name}>
                          {name ?? 'deleted'}
                        </Chip>
                      );
                    })}
                  </span>
                ),
            },
            {
              title: 'Last evaluated',
              width: 170,
              render: (_, rule) =>
                rule.lastError ? (
                  <span className="flex items-center gap-1.5">
                    <Tooltip title={rule.lastError}>
                      <Pill tone="bad" tabIndex={0}>
                        error
                      </Pill>
                    </Tooltip>
                    {rule.lastEvalAt && (
                      <span className="text-xs text-gray-500 dark:text-gray-400">
                        {dayjs(rule.lastEvalAt).fromNow()}
                      </span>
                    )}
                  </span>
                ) : rule.lastEvalAt ? (
                  <span className="text-gray-600 dark:text-gray-300">
                    {dayjs(rule.lastEvalAt).fromNow()}
                  </span>
                ) : (
                  <span className="text-xs text-gray-500 dark:text-gray-400">not yet</span>
                ),
            },
            {
              title: 'Fired, 7 days',
              width: 130,
              render: (_, rule) =>
                activity ? (
                  <FiredBarsComponent
                    days={ruleDays(activity, rule)}
                    scaleMax={firedScale}
                    paused={!rule.enabled}
                  />
                ) : (
                  <span className="text-xs text-gray-500 dark:text-gray-400">n/a</span>
                ),
            },
            {
              title: '',
              width: 160,
              render: (_, rule) => (
                <div className="flex justify-end gap-2">
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
      </Card>
      <Card
        title="Noisiest rules"
        subtitle={`Notifications sent in the last 7 days, with how often each rule fired; under ${NOISY_PER_FIRING} per firing is calm${countingSince ? ` (counting since ${countingSince})` : ''}`}
      >
        <div className="flex flex-col gap-3">
          <RankedList
            empty={activity ? 'No notifications in the last 7 days' : 'No data'}
            rows={noisy.map((n) => {
              const rule = rules.find((r) => r.id === n.ruleId);
              return {
                key: n.ruleId,
                label: n.name,
                value: n.notifications,
                extra:
                  n.perFiring === null
                    ? 'no new firings'
                    : `fired ${n.fired.toLocaleString()}× · ${n.perFiring.toFixed(1)} per firing`,
                color:
                  n.perFiring === null || n.perFiring > NOISY_PER_FIRING
                    ? 'var(--v2-orange)'
                    : 'var(--v2-accent)',
                onClick: rule && !isBudgetRule(rule) ? () => setEditing(rule) : undefined,
              };
            })}
          />
          <p className="m-0 text-xs text-gray-500 dark:text-gray-400">
            Pausing a noisy rule keeps its history; edit its &quot;For (s)&quot; or &quot;Remind
            every (s)&quot; to calm it instead.
          </p>
        </div>
      </Card>
      {editing && (
        <RuleEditorDrawerComponent
          projectId={projectId}
          rule={editing === 'new' ? null : editing}
          draft={editing === 'new' ? draft : null}
          channels={channels}
          canEdit={canEdit && !(editing !== 'new' && editing.managedBy)}
          onClose={() => {
            setEditing(null);
            setDraft(null);
          }}
          onSaved={(saved) => {
            setDraft(null);
            setRules((prev) =>
              prev.some((r) => r.id === saved.id)
                ? prev.map((r) => (r.id === saved.id ? saved : r))
                : [...prev, saved],
            );
            setEditing(null);
            loadActivity();
            onChanged?.();
          }}
        />
      )}
    </div>
  );
};
