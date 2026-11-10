// Posture → Recommendations: ranked actions (coverage gaps first, then the most severity closed
// per unit of effort), guardrails, and what each standard would score with the plan done.
import { LoadingOutlined } from '@ant-design/icons';
import { App, Checkbox, Empty, Progress, Segmented, Spin, Table, Tag, Tooltip } from 'antd';
import { useEffect, useMemo, useState } from 'react';

import { postureApi } from '../../../entity/posture';
import { type CostSite, SiteErrors, type SiteOutcome, fanOut } from '../../cost';
import { Panel } from '../../synthetics';
import {
  type ActionFilter,
  KIND_LABEL,
  STATUS_LABEL,
  type SiteAction,
  actionId,
  matchesFilter,
  mergeActions,
} from '../lib/actions';
import { ActionDrawer } from './ActionDrawer';
import { FindingDrawer } from './FindingDrawer';
import { SeverityTag } from './parts';

interface Props {
  sites: CostSite[];
  refresh: number;
}

const scoreColor = (percent: number) =>
  percent >= 80 ? '#009966' : percent >= 50 ? '#f59e0b' : '#dc2626';

export const ActionsTab = ({ sites, refresh }: Props) => {
  const { message } = App.useApp();
  const [data, setData] = useState<
    ReturnType<typeof mergeActions> & { outcomes: SiteOutcome<unknown>[] }
  >();
  const [filter, setFilter] = useState<ActionFilter>('todo');
  const [open, setOpen] = useState<SiteAction>();
  const [finding, setFinding] = useState<{ siteId: string; id: string }>();
  const [reload, setReload] = useState(0);
  const [busy, setBusy] = useState<string>();

  useEffect(() => {
    const controller = new AbortController();
    void fanOut(sites, (siteId) => postureApi.actions(siteId, controller.signal)).then(
      (outcomes) => {
        if (!controller.signal.aborted) setData({ ...mergeActions(outcomes), outcomes });
      },
    );
    return () => controller.abort();
  }, [sites, refresh, reload]);

  const visible = useMemo(
    () => (data?.actions ?? []).filter((action) => matchesFilter(action, filter)),
    [data, filter],
  );

  if (!data) {
    return (
      <div className="flex justify-center py-16">
        <Spin indicator={<LoadingOutlined spin />} />
      </div>
    );
  }

  const togglePlan = async (action: SiteAction, planned: boolean) => {
    setBusy(actionId(action));
    try {
      await postureApi.updateAction(action.siteId, {
        projectId: action.projectId,
        key: action.key,
        planned,
      });
      void message.success(planned ? 'Added to the plan' : 'Taken out of the plan');
      setReload((value) => value + 1);
    } catch (reason) {
      void message.error((reason as Error).message);
    } finally {
      setBusy(undefined);
    }
  };

  const inPlan = data.actions.filter((action) => action.planned && action.status === 'open');
  const counts: Record<ActionFilter, number> = {
    todo: 0,
    plan: 0,
    done: 0,
    accepted_risk: 0,
    dismissed: 0,
    all: data.actions.length,
  };
  for (const action of data.actions) {
    for (const key of ['todo', 'plan', 'done', 'accepted_risk', 'dismissed'] as ActionFilter[]) {
      if (matchesFilter(action, key)) counts[key] += 1;
    }
  }

  return (
    <div className="flex flex-col gap-4">
      <SiteErrors outcomes={data.outcomes} />
      {data.standards.length > 0 && (
        <div className="grid grid-cols-1 gap-3 md:grid-cols-2 xl:grid-cols-4">
          {data.standards.map((score) => (
            <div
              key={score.standard}
              className="flex flex-col gap-2 rounded-lg border border-gray-200 p-3 dark:border-gray-700"
            >
              <span className="text-sm font-medium text-gray-900 dark:text-gray-100">
                {score.name}
              </span>
              <span className="flex items-center gap-3">
                <Progress
                  type="circle"
                  size={52}
                  percent={score.current}
                  strokeColor={scoreColor(score.current)}
                  format={(value) => `${Math.round(value ?? 0)}%`}
                />
                <span className="flex flex-col text-xs text-gray-600 dark:text-gray-300">
                  <span>
                    With the plan done:{' '}
                    <b style={{ color: scoreColor(score.withPlan) }}>
                      {Math.round(score.withPlan)}%
                    </b>
                  </span>
                  <span>
                    With everything done:{' '}
                    <b style={{ color: scoreColor(score.withAll) }}>{Math.round(score.withAll)}%</b>
                  </span>
                  <span className="text-gray-400">{score.evaluated} controls evaluated</span>
                </span>
              </span>
            </div>
          ))}
        </div>
      )}

      <Panel
        title="Actions"
        subtitle={`Coverage gaps first, then the most severity closed per unit of effort. ${inPlan.length} in the plan.`}
        extra={
          <Segmented
            size="small"
            value={filter}
            onChange={(value) => setFilter(value as ActionFilter)}
            options={[
              { value: 'todo', label: `To do (${counts.todo})` },
              { value: 'plan', label: `Plan (${counts.plan})` },
              { value: 'done', label: `Done (${counts.done})` },
              { value: 'accepted_risk', label: `Accepted risk (${counts.accepted_risk})` },
              { value: 'dismissed', label: `Dismissed (${counts.dismissed})` },
              { value: 'all', label: 'All' },
            ]}
          />
        }
      >
        {data.actions.length === 0 ? (
          <Empty
            image={Empty.PRESENTED_IMAGE_SIMPLE}
            description="Nothing to do: no open findings or coverage gaps."
          />
        ) : (
          <Table
            size="small"
            rowKey={actionId}
            dataSource={visible}
            pagination={visible.length > 20 ? { pageSize: 20 } : false}
            scroll={{ x: 960 }}
            onRow={(action) => ({ onClick: () => setOpen(action), className: 'cursor-pointer' })}
            columns={[
              {
                title: <Tooltip title="In the plan">Plan</Tooltip>,
                key: 'plan',
                width: 56,
                render: (_, action) => (
                  <span onClick={(event) => event.stopPropagation()}>
                    <Checkbox
                      aria-label={`Plan: ${action.title}`}
                      checked={action.planned}
                      disabled={
                        action.dismissed || action.status !== 'open' || busy === actionId(action)
                      }
                      onChange={(event) => void togglePlan(action, event.target.checked)}
                    />
                  </span>
                ),
              },
              {
                title: 'Action',
                key: 'title',
                render: (_, action) => (
                  <span className="flex flex-col">
                    <span className="text-gray-900 dark:text-gray-100">{action.title}</span>
                    {action.detail && (
                      <span className="line-clamp-1 text-xs text-gray-500 dark:text-gray-400">
                        {action.detail}
                      </span>
                    )}
                  </span>
                ),
              },
              {
                title: 'Kind',
                key: 'kind',
                width: 120,
                render: (_, action) => (
                  <Tag
                    className="m-0"
                    color={
                      action.kind === 'coverage'
                        ? 'orange'
                        : action.kind === 'guardrail'
                          ? 'blue'
                          : 'default'
                    }
                  >
                    {KIND_LABEL[action.kind]}
                  </Tag>
                ),
              },
              {
                title: 'Severity',
                key: 'severity',
                width: 110,
                render: (_, action) =>
                  action.severity ? <SeverityTag severity={action.severity} /> : '—',
              },
              {
                title: 'Closes',
                key: 'closes',
                width: 80,
                align: 'right' as const,
                render: (_, action) => (action.findings ?? []).length || '—',
              },
              { title: 'Effort', key: 'effort', width: 80, render: (_, action) => action.effort },
              {
                title: 'Where',
                key: 'where',
                width: 180,
                render: (_, action) => (
                  <span className="flex flex-col text-xs">
                    <span>{action.connectionName || '—'}</span>
                    <span className="text-gray-500 dark:text-gray-400">
                      {[action.account, action.region].filter(Boolean).join(' · ') ||
                        (action.scopes
                          ? `${action.scopes.length} ${action.scopes.length === 1 ? 'scope' : 'scopes'}`
                          : '')}
                    </span>
                  </span>
                ),
              },
              {
                title: 'Status',
                key: 'status',
                width: 120,
                render: (_, action) =>
                  action.dismissed ? (
                    <Tag className="m-0">Dismissed</Tag>
                  ) : (
                    <Tag
                      className="m-0"
                      color={
                        action.status === 'done'
                          ? 'green'
                          : action.status === 'accepted_risk'
                            ? 'purple'
                            : action.planned
                              ? 'gold'
                              : 'default'
                      }
                    >
                      {action.status === 'open' && action.planned
                        ? 'Planned'
                        : STATUS_LABEL[action.status]}
                    </Tag>
                  ),
              },
            ]}
          />
        )}
        <p className="mb-0 text-xs text-gray-500 dark:text-gray-400">
          Tick actions to add them to the plan; the cards above show each standard&apos;s score with
          the plan done. Obzly is read-only: the changes are made by your team.
        </p>
      </Panel>

      {open && (
        <ActionDrawer
          action={open}
          onClose={() => setOpen(undefined)}
          onChanged={(updated) => {
            setOpen(updated);
            setReload((value) => value + 1);
          }}
          onFinding={(siteId, id) => setFinding({ siteId, id })}
        />
      )}
      {finding && (
        <FindingDrawer
          siteId={finding.siteId}
          findingId={finding.id}
          onClose={() => setFinding(undefined)}
          onChanged={() => setReload((value) => value + 1)}
        />
      )}
    </div>
  );
};
