// Existing Savings Plans and reservations: how much each was used over 30 days, flagged when
// unused or ending within 60 days (that also alerts, on the cost alert channels).
import { Empty, Progress, Table, Tag, Tooltip } from 'antd';
import dayjs from 'dayjs';

import type { CommitmentCoverage } from '../../../entity/cost';
import { Panel } from '../../synthetics';
import { PAYMENT_LABEL, type SiteCommitment, TERM_LABEL, TYPE_LABEL } from '../lib/advice';
import { formatMoney } from '../lib/format';
import type { CostSite } from '../lib/merge';

interface Props {
  commitments: SiteCommitment[];
  coverage: (CommitmentCoverage & CostSite)[];
}

export const CommitmentsPanel = ({ commitments, coverage }: Props) => (
  <Panel
    title="Existing commitments"
    subtitle="Use over the last 30 days. Ending within 60 days alerts on the cost alert channels."
  >
    {coverage.length > 0 && (
      <div className="mb-3 flex flex-wrap gap-2">
        {coverage.map((row) => (
          <Tooltip
            key={`${row.siteId}|${row.connectionId}|${row.type}`}
            title={
              row.onDemandMonthly !== null && row.currency
                ? `${formatMoney(row.onDemandMonthly, row.currency)} a month still on demand`
                : undefined
            }
          >
            <span className="rounded bg-gray-100 px-2 py-1 text-xs text-gray-700 dark:bg-gray-700 dark:text-gray-200">
              {TYPE_LABEL[row.type]} cover <b>{row.coveredPercent.toFixed(0)}%</b> of eligible usage
              · {row.connectionName}
            </span>
          </Tooltip>
        ))}
      </div>
    )}
    {commitments.length === 0 ? (
      <Empty
        image={Empty.PRESENTED_IMAGE_SIMPLE}
        description="No Savings Plans or reservations found, or none readable yet"
      />
    ) : (
      <Table
        size="small"
        rowKey={(row) => `${row.siteId}|${row.connectionId}|${row.id}`}
        dataSource={commitments}
        pagination={commitments.length > 10 ? { pageSize: 10 } : false}
        scroll={{ x: 900 }}
        columns={[
          {
            title: 'Commitment',
            key: 'what',
            render: (_, row) => (
              <span className="flex flex-col">
                <span className="text-gray-900 dark:text-gray-100">{row.description}</span>
                <span className="text-xs text-gray-500 dark:text-gray-400">
                  {TYPE_LABEL[row.type]} · {row.connectionName}
                  {row.region ? ` · ${row.region}` : ''}
                </span>
              </span>
            ),
          },
          {
            title: 'Term',
            key: 'term',
            width: 170,
            render: (_, row) =>
              [
                row.term ? TERM_LABEL[row.term as '1y'] : '',
                row.payment ? (PAYMENT_LABEL[row.payment as 'no_upfront'] ?? row.payment) : '',
              ]
                .filter(Boolean)
                .join(' · ') || '—',
          },
          {
            title: 'Ends',
            key: 'ends',
            width: 140,
            sorter: (a, b) => (a.endsAt ?? '9').localeCompare(b.endsAt ?? '9'),
            render: (_, row) =>
              row.endsAt ? (
                <span className="flex flex-col">
                  <span>{dayjs(row.endsAt).format('MMM D, YYYY')}</span>
                  {row.endsInDays !== undefined && row.endsInDays >= 0 && (
                    <span className="text-xs text-gray-500 dark:text-gray-400">
                      in {row.endsInDays} days
                    </span>
                  )}
                </span>
              ) : (
                '—'
              ),
          },
          {
            title: 'Used (30 days)',
            key: 'used',
            width: 160,
            render: (_, row) =>
              row.utilization === null ? (
                <Tooltip title="The provider didn't say how much of it was used">
                  <span className="text-gray-400">n/a</span>
                </Tooltip>
              ) : (
                <Progress
                  size="small"
                  percent={Math.round(row.utilization * 10) / 10}
                  strokeColor={
                    row.utilization >= 90
                      ? '#009966'
                      : row.utilization >= 60
                        ? '#f59e0b'
                        : '#dc2626'
                  }
                />
              ),
          },
          {
            title: 'Unused / month',
            key: 'unused',
            width: 130,
            align: 'right' as const,
            render: (_, row) =>
              row.unusedMonthly !== null && row.currency
                ? formatMoney(row.unusedMonthly, row.currency)
                : '—',
          },
          {
            title: 'Status',
            key: 'status',
            width: 140,
            render: (_, row) => (
              <span className="flex flex-wrap gap-1">
                {row.unused && (
                  <Tooltip title="Nothing used it in 30 days: the usage it was bought for has moved or stopped">
                    <Tag color="red">Unused</Tag>
                  </Tooltip>
                )}
                {row.endingSoon && (
                  <Tooltip title="Renew or replace it before then, or that usage goes back to on-demand prices">
                    <Tag color="orange">Ends soon</Tag>
                  </Tooltip>
                )}
                {!row.unused && !row.endingSoon && <Tag color="green">OK</Tag>}
              </span>
            ),
          },
        ]}
      />
    )}
  </Panel>
);
