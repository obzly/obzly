import { DeleteOutlined, EditOutlined, LoadingOutlined, PlusOutlined } from '@ant-design/icons';
import { Alert, App, Button, Popconfirm, Spin, Tag, Tooltip } from 'antd';
import React, { useEffect, useState } from 'react';

import {
  type AiBudget,
  type AiBudgetRequest,
  type AiBudgetStatus,
  aiBudgetsApi,
} from '../../../entity/aiUsage';
import { type NotificationChannel, alertingApi } from '../../../entity/alerting';
import {
  BUDGET_STATE_COLORS,
  BUDGET_STATE_LABELS,
  budgetState,
  forecastLabel,
  isPerGroup,
  periodLabel,
  progressPercent,
  resetLabel,
  scopeLabel,
  spentLabel,
} from '../lib/budgets';
import { formatUsd } from '../lib/format';
import { BudgetEditorDrawer } from './BudgetEditorDrawer';

interface Props {
  projectId: string;
  canManage: boolean;
}

const errorText = (error: unknown, fallback: string) =>
  error instanceof Error ? error.message : fallback;

const topTitle = (budget: AiBudget): string =>
  budget.scope === 'each_user' ? 'Top users' : 'Top models';

const BudgetCard = ({
  budget,
  status,
  channels,
  canManage,
  onEdit,
  onDelete,
}: {
  budget: AiBudget;
  status?: AiBudgetStatus;
  channels: NotificationChannel[];
  canManage: boolean;
  onEdit: () => void;
  onDelete: () => void;
}) => {
  const percent = status?.percent ?? 0;
  const state = budgetState(percent, budget.thresholds);
  const color = BUDGET_STATE_COLORS[state];
  const firstThreshold = Math.min(...budget.thresholds, 100);
  const channelNames = budget.channelIds
    .map((id) => channels.find((channel) => channel.id === id)?.name)
    .filter(Boolean);
  const showTop = !!status && status.top.length > 0 && budget.scope !== 'model';

  return (
    <article className="flex flex-col gap-3 rounded-lg border border-gray-200 bg-white p-4 dark:border-gray-700 dark:bg-gray-800">
      <div className="flex items-start justify-between gap-2">
        <div className="min-w-0">
          <h3 className="m-0 truncate text-[15px] font-semibold text-gray-900 dark:text-gray-100">
            {budget.name}
          </h3>
          <div className="text-xs text-gray-500 dark:text-gray-400">{scopeLabel(budget)}</div>
        </div>
        {canManage && (
          <div className="flex gap-1">
            <Tooltip title="Edit">
              <Button
                type="text"
                size="small"
                icon={<EditOutlined />}
                aria-label="Edit budget"
                onClick={onEdit}
              />
            </Tooltip>
            <Popconfirm
              title={`Delete ${budget.name}?`}
              description="Its alert rule is removed too."
              okText="Delete"
              okButtonProps={{ danger: true }}
              onConfirm={onDelete}
            >
              <Tooltip title="Delete">
                <Button
                  type="text"
                  size="small"
                  danger
                  icon={<DeleteOutlined />}
                  aria-label="Delete budget"
                />
              </Tooltip>
            </Popconfirm>
          </div>
        )}
      </div>

      {status?.error ? (
        <Alert type="warning" showIcon title={status.error} />
      ) : (
        <>
          <div className="flex flex-wrap items-baseline gap-2">
            <span className="text-2xl font-semibold text-gray-900 tabular-nums dark:text-gray-100">
              {status ? spentLabel(budget, status) : '-'}
            </span>
            <span className="text-gray-500 dark:text-gray-400">
              of {formatUsd(budget.amountUsd)} {isPerGroup(budget.scope) ? 'each, ' : ''}
              {periodLabel(budget.period)}
            </span>
            <Tag className="!ml-auto" color={color}>
              {BUDGET_STATE_LABELS[state]}
            </Tag>
          </div>
          <div
            role="img"
            aria-label={`${percent.toFixed(0)}% of the budget spent`}
            className="relative h-2.5 rounded bg-gray-100 dark:bg-gray-700"
          >
            <div
              className="h-2.5 rounded"
              style={{ width: `${progressPercent(percent)}%`, background: color }}
            />
            {firstThreshold < 100 && (
              <div
                className="absolute -top-1 h-4 w-0.5 bg-gray-500"
                style={{ left: `${firstThreshold}%` }}
              />
            )}
          </div>
          {status && (
            <div className="flex flex-wrap justify-between gap-2 text-xs text-gray-500 dark:text-gray-400">
              <span>{forecastLabel(budget, status)}</span>
              <span>{resetLabel(status, budget.timeZone)}</span>
            </div>
          )}
          {showTop && status && (
            <div className="flex flex-col gap-1.5 border-t border-gray-100 pt-2 dark:border-gray-700">
              <span className="text-xs font-semibold text-gray-500 uppercase dark:text-gray-400">
                {topTitle(budget)}
              </span>
              {status.top.map((group) => {
                const groupColor = isPerGroup(budget.scope)
                  ? BUDGET_STATE_COLORS[budgetState(group.percent, budget.thresholds)]
                  : '#2563eb';
                return (
                  <div key={group.key} className="flex items-center gap-2 text-sm">
                    <span className="w-44 truncate text-gray-800 dark:text-gray-200">
                      {group.key}
                    </span>
                    <div className="h-1.5 flex-1 rounded bg-gray-100 dark:bg-gray-700">
                      <div
                        className="h-1.5 rounded"
                        style={{
                          width: `${progressPercent(group.percent)}%`,
                          background: groupColor,
                        }}
                      />
                    </div>
                    <span className="w-32 text-right font-mono text-xs text-gray-700 dark:text-gray-300">
                      {formatUsd(group.spentUsd)}
                      {isPerGroup(budget.scope) ? ` (${group.percent.toFixed(0)}%)` : ''}
                    </span>
                  </div>
                );
              })}
            </div>
          )}
        </>
      )}
      <div className="text-xs text-gray-500 dark:text-gray-400">
        {channelNames.length ? `Notifies ${channelNames.join(', ')}` : 'No notification channels'}
        {' · '}
        alerts at {budget.thresholds.map((t) => `${t}%`).join(', ')}
      </div>
    </article>
  );
};

export const BudgetsComponent = ({ projectId, canManage }: Props): React.JSX.Element => {
  const { message } = App.useApp();

  const [budgets, setBudgets] = useState<AiBudget[]>([]);
  const [statuses, setStatuses] = useState<AiBudgetStatus[]>([]);
  const [channels, setChannels] = useState<NotificationChannel[]>([]);
  const [isLoading, setIsLoading] = useState(true);
  const [loadError, setLoadError] = useState<string | null>(null);
  const [editing, setEditing] = useState<AiBudget | 'new' | null>(null);

  const load = async () => {
    try {
      const [loadedBudgets, loadedChannels] = await Promise.all([
        aiBudgetsApi.listBudgets(projectId),
        alertingApi.listChannels(projectId),
      ]);
      setBudgets(loadedBudgets);
      setChannels(loadedChannels);
      setLoadError(null);
      setStatuses(loadedBudgets.length ? await aiBudgetsApi.getStatus(projectId) : []);
    } catch (error: unknown) {
      setLoadError(errorText(error, 'Failed to load budgets'));
    } finally {
      setIsLoading(false);
    }
  };

  const save = async (request: AiBudgetRequest) => {
    if (editing && editing !== 'new') {
      await aiBudgetsApi.updateBudget(projectId, editing.id, request);
      message.success('Budget saved');
    } else {
      await aiBudgetsApi.createBudget(projectId, request);
      message.success('Budget created');
    }
    setEditing(null);
    await load();
  };

  const remove = async (budget: AiBudget) => {
    try {
      await aiBudgetsApi.deleteBudget(projectId, budget.id);
      message.success('Budget deleted');
      await load();
    } catch (error: unknown) {
      message.error(errorText(error, 'Failed to delete budget'));
    }
  };

  useEffect(() => {
    setIsLoading(true);
    void load();
  }, [projectId]);

  return (
    <div className="flex flex-col gap-4">
      <div className="flex flex-wrap items-center gap-3">
        <Alert
          className="mr-auto"
          type="info"
          title="Budgets alert; they can't stop spending. Spend uses the same estimates as the Claude Code and LLM apps tabs, checked every 5 minutes."
        />
        {canManage && (
          <Button
            type="primary"
            icon={<PlusOutlined />}
            onClick={() => setEditing('new')}
            className="border-emerald-600 bg-emerald-600 hover:border-emerald-700 hover:bg-emerald-700"
          >
            New budget
          </Button>
        )}
      </div>

      {loadError && <Alert type="error" showIcon title={loadError} />}

      {isLoading ? (
        <div className="flex justify-center p-12">
          <Spin indicator={<LoadingOutlined spin />} />
        </div>
      ) : budgets.length === 0 ? (
        <div className="rounded-lg border border-dashed border-gray-300 p-8 text-center text-sm text-gray-500 dark:border-gray-600 dark:text-gray-400">
          No budgets yet.{' '}
          {canManage
            ? 'Set a monthly limit for the project, or a daily one for each user.'
            : 'Project owners and admins can add them.'}
        </div>
      ) : (
        <div className="grid grid-cols-1 gap-4 xl:grid-cols-2">
          {budgets.map((budget) => (
            <BudgetCard
              key={budget.id}
              budget={budget}
              status={statuses.find((status) => status.budgetId === budget.id)}
              channels={channels}
              canManage={canManage}
              onEdit={() => setEditing(budget)}
              onDelete={() => remove(budget)}
            />
          ))}
        </div>
      )}

      {editing && (
        <BudgetEditorDrawer
          budget={editing === 'new' ? null : editing}
          channels={channels}
          onClose={() => setEditing(null)}
          onSave={save}
        />
      )}
    </div>
  );
};
