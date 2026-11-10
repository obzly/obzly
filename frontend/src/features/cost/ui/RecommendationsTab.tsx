// Cost → Recommendations: what to change to save money (rightsizing, idle, modernize, storage),
// the commitments you have and the ones to consider, and what changes actually saved. Read weekly
// by the probe; Obzly never buys or changes anything.
import { LoadingOutlined } from '@ant-design/icons';
import { Alert, Empty, Segmented, Select, Spin, Table, Tag, Tooltip } from 'antd';
import dayjs from 'dayjs';
import { useEffect, useMemo, useState } from 'react';

import { type RecommendationKind, costApi } from '../../../entity/cost';
import { Panel } from '../../synthetics';
import {
  KIND_LABEL,
  SECTION_LABEL,
  SOURCE_LABEL,
  STATE_LABEL,
  STATUS_LABEL,
  type SiteCommitment,
  type SitePlan,
  type SiteRecommendation,
  type SiteSection,
  isActive,
  mergeCommitments,
  mergePlans,
  mergeRecommendations,
  sectionProblems,
  totalsByCurrency,
} from '../lib/advice';
import { formatMoney } from '../lib/format';
import type { CostSite, SiteOutcome } from '../lib/merge';
import { fanOut } from '../lib/sites';
import { CommitmentsPanel } from './CommitmentsPanel';
import { PlannerPanel } from './PlannerPanel';
import { RecommendationDrawer } from './RecommendationDrawer';
import { SiteErrors, Tile } from './parts';

interface Props {
  sites: CostSite[];
  refresh: number;
}

type StatusFilter = 'active' | 'done' | 'dismissed' | 'all';

interface Data {
  items: SiteRecommendation[];
  sections: SiteSection[];
  commitments: SiteCommitment[];
  coverage: ReturnType<typeof mergeCommitments>['coverage'];
  outcomes: SiteOutcome<unknown>[];
}

const itemId = (item: SiteRecommendation) => `${item.siteId}|${item.projectId}|${item.key}`;

const Totals = ({
  totals,
  unknown,
}: {
  totals: { currency: string; amount: number }[];
  unknown?: number;
}) =>
  totals.length === 0 ? (
    <span className="text-gray-400">{unknown ? 'n/a' : '—'}</span>
  ) : (
    <span className="flex flex-col">
      {totals.map((total) => (
        <span key={total.currency}>{formatMoney(total.amount, total.currency)}</span>
      ))}
    </span>
  );

export const RecommendationsTab = ({ sites, refresh }: Props) => {
  const [data, setData] = useState<Data>();
  const [plans, setPlans] = useState<SitePlan[]>();
  const [lookback, setLookback] = useState(30);
  const [rightsizingFirst, setRightsizingFirst] = useState(true);
  const [status, setStatus] = useState<StatusFilter>('active');
  const [kinds, setKinds] = useState<RecommendationKind[]>([]);
  const [accounts, setAccounts] = useState<string[]>([]);
  const [open, setOpen] = useState<SiteRecommendation>();
  // Plans and dismissals change what rightsizing first takes off the planner.
  const [changes, setChanges] = useState(0);

  useEffect(() => {
    const controller = new AbortController();
    void Promise.all([
      fanOut(sites, (siteId) => costApi.recommendations(siteId, controller.signal)),
      fanOut(sites, (siteId) => costApi.commitments(siteId, controller.signal)),
    ]).then(([recommendations, commitments]) => {
      if (controller.signal.aborted) return;
      const merged = mergeRecommendations(recommendations);
      setData({ ...merged, ...mergeCommitments(commitments), outcomes: recommendations });
    });
    return () => controller.abort();
  }, [sites, refresh]);

  useEffect(() => {
    const controller = new AbortController();
    void fanOut(sites, (siteId) =>
      costApi.planner(siteId, lookback, rightsizingFirst, controller.signal),
    ).then((outcomes) => {
      if (!controller.signal.aborted) setPlans(mergePlans(outcomes));
    });
    return () => controller.abort();
  }, [sites, refresh, lookback, rightsizingFirst, changes]);

  const visible = useMemo(
    () =>
      (data?.items ?? []).filter((item) => {
        if (status === 'active' && !isActive(item)) return false;
        if (status !== 'active' && status !== 'all' && item.status !== status) return false;
        if (kinds.length > 0 && !kinds.includes(item.kind)) return false;
        return accounts.length === 0 || accounts.includes(item.account);
      }),
    [data, status, kinds, accounts],
  );

  if (!data) {
    return (
      <div className="flex justify-center py-16">
        <Spin indicator={<LoadingOutlined spin />} />
      </div>
    );
  }

  const active = data.items.filter(isActive);
  const openTotals = totalsByCurrency(
    active.filter((item) => item.status === 'open'),
    (item) => item.monthlySavings,
    (item) => item.currency,
  );
  const plannedTotals = totalsByCurrency(
    active.filter((item) => item.status === 'planned'),
    (item) => item.monthlySavings,
    (item) => item.currency,
  );
  const done = data.items.filter((item) => item.status === 'done');
  const realizedTotals = totalsByCurrency(
    done,
    (item) => item.realizedMonthly,
    (item) => item.currency,
  );
  const flagged = data.commitments.filter((item) => item.unused || item.endingSoon);
  const problems = sectionProblems(data.sections);
  const accountOptions = [...new Set(data.items.map((item) => item.account).filter(Boolean))].map(
    (account) => ({ value: account, label: account }),
  );
  const replace = (updated: SiteRecommendation) => {
    setChanges((value) => value + 1);
    setData((current) =>
      current
        ? {
            ...current,
            items: current.items.map((item) => (itemId(item) === itemId(updated) ? updated : item)),
          }
        : current,
    );
  };

  return (
    <div className="flex flex-col gap-4">
      <SiteErrors outcomes={data.outcomes} />
      {problems.length > 0 && (
        <Alert
          type="warning"
          showIcon
          title="Some recommendations couldn't be read; their last good data is shown"
          description={
            <ul className="m-0 pl-4 text-xs">
              {problems.map((section) => (
                <li key={`${section.siteId}|${section.connectionId}|${section.section}`}>
                  {section.connectionName || 'A connection'} · {SECTION_LABEL[section.section]}:{' '}
                  {STATE_LABEL[section.state]}
                  {section.reason ? ` (${section.reason})` : ''}
                </li>
              ))}
            </ul>
          }
        />
      )}

      <div className="grid grid-cols-2 gap-4 xl:grid-cols-4">
        <Tile
          label="Open savings / month"
          value={<Totals {...openTotals} />}
          hint={`${active.filter((item) => item.status === 'open').length} recommendations${openTotals.unknown ? `, ${openTotals.unknown} without a figure yet` : ''}`}
        />
        <Tile
          label="Planned / month"
          value={<Totals {...plannedTotals} />}
          hint={`${active.filter((item) => item.status === 'planned').length} in the plan`}
        />
        <Tile
          label="Realized / month"
          tooltip="Median daily cost 14 days before each change vs 14 days after, from cost data"
          value={<Totals {...realizedTotals} />}
          hint={`${done.length} done in the last 180 days`}
        />
        <Tile
          label="Commitments to check"
          value={flagged.length}
          hint={`${data.commitments.filter((item) => item.unused).length} unused, ${data.commitments.filter((item) => item.endingSoon).length} ending soon`}
        />
      </div>

      {plans ? (
        <PlannerPanel
          plans={plans}
          lookback={lookback}
          onLookback={setLookback}
          rightsizingFirst={rightsizingFirst}
          onRightsizingFirst={setRightsizingFirst}
        />
      ) : (
        <Panel title="Commitment planner">
          <div className="flex justify-center py-8">
            <Spin indicator={<LoadingOutlined spin />} />
          </div>
        </Panel>
      )}

      <CommitmentsPanel commitments={data.commitments} coverage={data.coverage} />

      <Panel
        title="Recommendations"
        subtitle="Largest savings first. Click one for the evidence and to plan, finish or dismiss it."
        extra={
          <span className="flex flex-wrap items-center gap-2">
            <Select
              size="small"
              mode="multiple"
              allowClear
              placeholder="Kind"
              className="min-w-36"
              value={kinds}
              onChange={setKinds}
              options={(Object.keys(KIND_LABEL) as RecommendationKind[]).map((kind) => ({
                value: kind,
                label: KIND_LABEL[kind],
              }))}
            />
            {accountOptions.length > 1 && (
              <Select
                size="small"
                mode="multiple"
                allowClear
                placeholder="Account"
                className="min-w-40"
                value={accounts}
                onChange={setAccounts}
                options={accountOptions}
              />
            )}
            <Segmented
              size="small"
              value={status}
              onChange={(value) => setStatus(value as StatusFilter)}
              options={[
                { value: 'active', label: 'To do' },
                { value: 'done', label: 'Done' },
                { value: 'dismissed', label: 'Dismissed' },
                { value: 'all', label: 'All' },
              ]}
            />
          </span>
        }
      >
        {data.items.length === 0 ? (
          <Empty
            image={Empty.PRESENTED_IMAGE_SIMPLE}
            description="No recommendations yet. The probe reads them weekly for connections that read cost."
          />
        ) : (
          <Table
            size="small"
            rowKey={itemId}
            dataSource={visible}
            pagination={visible.length > 20 ? { pageSize: 20 } : false}
            scroll={{ x: 1000 }}
            onRow={(item) => ({ onClick: () => setOpen(item), className: 'cursor-pointer' })}
            columns={[
              {
                title: 'Change',
                key: 'action',
                render: (_, item) => (
                  <span className="flex flex-col">
                    <span className="text-gray-900 dark:text-gray-100">{item.action}</span>
                    <span className="text-xs text-gray-500 dark:text-gray-400">
                      {item.resourceName || item.resourceKey}
                    </span>
                  </span>
                ),
              },
              {
                title: 'Kind',
                key: 'kind',
                width: 110,
                render: (_, item) => KIND_LABEL[item.kind] ?? item.kind,
              },
              {
                title: 'Where',
                key: 'where',
                width: 190,
                render: (_, item) => (
                  <span className="flex flex-col text-xs">
                    <span>{item.connectionName || '—'}</span>
                    <span className="text-gray-500 dark:text-gray-400">
                      {[item.account, item.region].filter(Boolean).join(' · ')}
                    </span>
                  </span>
                ),
              },
              {
                title: 'Saves / month',
                key: 'savings',
                width: 130,
                align: 'right' as const,
                render: (_, item) =>
                  item.monthlySavings === null ? (
                    <Tooltip title="Needs resource-level cost from an export">
                      <span className="text-gray-400">n/a</span>
                    </Tooltip>
                  ) : (
                    <span className="tabular-nums">
                      {formatMoney(item.monthlySavings, item.currency)}
                    </span>
                  ),
              },
              {
                title: 'Effort',
                key: 'effort',
                width: 80,
                render: (_, item) => item.effort,
              },
              {
                title: 'Risk',
                key: 'risk',
                width: 80,
                render: (_, item) => (
                  <Tag
                    className="m-0"
                    color={
                      item.risk === 'high' ? 'red' : item.risk === 'medium' ? 'orange' : 'green'
                    }
                  >
                    {item.risk}
                  </Tag>
                ),
              },
              {
                title: 'Source',
                key: 'source',
                width: 140,
                render: (_, item) => (
                  <span className="text-xs">{SOURCE_LABEL[item.source] ?? item.source}</span>
                ),
              },
              {
                title: 'Status',
                key: 'status',
                width: 130,
                render: (_, item) => (
                  <span className="flex flex-col">
                    <Tag
                      className="m-0 w-fit"
                      color={
                        { open: 'default', planned: 'gold', done: 'green', dismissed: 'default' }[
                          item.status
                        ]
                      }
                    >
                      {STATUS_LABEL[item.status]}
                    </Tag>
                    {item.status === 'planned' && (item.owner || item.dueDate) && (
                      <span className="text-xs text-gray-500 dark:text-gray-400">
                        {[item.owner?.split('@')[0], item.dueDate].filter(Boolean).join(' · ')}
                      </span>
                    )}
                  </span>
                ),
              },
            ]}
          />
        )}
      </Panel>

      {done.length > 0 && (
        <Panel
          title="Realized savings"
          subtitle="Median daily cost 14 days before each change vs 14 days after, from cost data"
        >
          <Table
            size="small"
            rowKey={itemId}
            dataSource={done}
            pagination={done.length > 10 ? { pageSize: 10 } : false}
            onRow={(item) => ({ onClick: () => setOpen(item), className: 'cursor-pointer' })}
            columns={[
              {
                title: 'Change',
                key: 'action',
                render: (_, item) => (
                  <span className="flex flex-col">
                    <span>{item.action}</span>
                    <span className="text-xs text-gray-500 dark:text-gray-400">
                      {item.resourceName || item.resourceKey}
                    </span>
                  </span>
                ),
              },
              {
                title: 'Done',
                key: 'done',
                width: 170,
                render: (_, item) => (
                  <span className="flex flex-col text-xs">
                    <span>{item.doneAt ? dayjs(item.doneAt).format('MMM D, YYYY') : '—'}</span>
                    <span className="text-gray-500 dark:text-gray-400">
                      {item.doneBy === 'detected' ? 'Detected' : item.doneBy}
                    </span>
                  </span>
                ),
              },
              {
                title: 'Expected / month',
                key: 'expected',
                width: 140,
                align: 'right' as const,
                render: (_, item) =>
                  item.expectedMonthly !== null && item.expectedMonthly !== undefined
                    ? formatMoney(item.expectedMonthly, item.currency)
                    : 'n/a',
              },
              {
                title: 'Measured / month',
                key: 'measured',
                width: 160,
                align: 'right' as const,
                render: (_, item) =>
                  item.realizedMonthly !== null && item.realizedMonthly !== undefined ? (
                    <b className="tabular-nums">
                      {formatMoney(item.realizedMonthly, item.currency)}
                    </b>
                  ) : item.measuredAt ? (
                    <Tooltip title="Not enough resource-level cost around the change">
                      <span className="text-gray-400">n/a</span>
                    </Tooltip>
                  ) : (
                    <span className="text-xs text-gray-500 dark:text-gray-400">
                      from {dayjs(item.doneAt).add(16, 'day').format('MMM D')}
                    </span>
                  ),
              },
            ]}
          />
        </Panel>
      )}

      {open && (
        <RecommendationDrawer
          item={open}
          onClose={() => setOpen(undefined)}
          onChanged={(updated) => {
            replace(updated);
            setOpen(updated);
          }}
        />
      )}
    </div>
  );
};
