// The commitment planner: the providers' own purchase recommendations, 1 or 3 years by payment,
// for one look-back. Rightsize first shrinks each plan by the open rightsizing savings on the usage
// it covers, so the plan and the rightsizing list never count the same saving twice.
import { Button, Empty, Modal, Segmented, Select, Switch, Tag, Tooltip } from 'antd';
import { useCallback, useEffect, useMemo, useState } from 'react';

import type { CommitmentType, PurchaseOption, UsagePoint } from '../../../entity/cost';
import { type ChartColors, EChart, Panel, baseOption } from '../../synthetics';
import {
  PAYMENTS,
  PAYMENT_LABEL,
  type SitePlan,
  TERMS,
  TERM_LABEL,
  TYPE_FLEXIBILITY,
  TYPE_LABEL,
  TYPE_SHORT,
  defaultOption,
  howToBuy,
  optionAt,
  paybackText,
} from '../lib/advice';
import { formatMoney, formatMoneyCompact } from '../lib/format';

interface Props {
  plans: SitePlan[];
  lookback: number;
  onLookback: (days: number) => void;
  rightsizingFirst: boolean;
  onRightsizingFirst: (on: boolean) => void;
}

const planId = (plan: SitePlan) => `${plan.siteId}|${plan.connectionId}`;

export const PlannerPanel = ({
  plans,
  lookback,
  onLookback,
  rightsizingFirst,
  onRightsizingFirst,
}: Props) => {
  const connections = useMemo(() => {
    const seen = new Map<string, string>();
    for (const plan of plans) seen.set(planId(plan), plan.connectionName);
    return [...seen.entries()].map(([value, label]) => ({ value, label }));
  }, [plans]);
  const [connection, setConnection] = useState<string>();
  const [kind, setKind] = useState<CommitmentType>();
  const [picked, setPicked] = useState<string>();
  const [buying, setBuying] = useState(false);

  const current = connection ?? connections[0]?.value;
  const forConnection = plans.filter((plan) => planId(plan) === current);
  const plan = forConnection.find((item) => item.type === kind) ?? forConnection[0];
  const option =
    plan?.options.find((item) => `${item.term}|${item.payment}` === picked && item.offered) ??
    (plan ? defaultOption(plan) : undefined);

  useEffect(() => setPicked(undefined), [plan?.type, current, lookback]);

  const money = (amount: number) => formatMoney(amount, plan?.currency || 'USD');
  if (plans.length === 0) {
    return (
      <Panel title="Commitment planner" subtitle="Savings Plans and reservations to consider">
        <Empty
          image={Empty.PRESENTED_IMAGE_SIMPLE}
          description="No purchase recommendations yet. They're read weekly for connections that read cost, once Cost Explorer (AWS) or Cost Management (Azure) has enough usage."
        />
      </Panel>
    );
  }

  const payments = PAYMENTS[plan?.provider ?? 'aws'] ?? PAYMENTS.aws;
  return (
    <Panel
      title="Commitment planner"
      subtitle={`The provider's purchase recommendations from the last ${lookback} days of usage, on top of what you already have`}
      extra={
        connections.length > 1 && (
          <Select
            size="small"
            className="w-56"
            value={current}
            onChange={setConnection}
            options={connections}
            aria-label="Connection"
          />
        )
      }
    >
      <div className="mb-3 flex flex-wrap items-center gap-3">
        <Segmented
          size="small"
          value={plan?.type}
          onChange={(value) => setKind(value as CommitmentType)}
          options={forConnection.map((item) => ({
            value: item.type,
            label: TYPE_SHORT[item.type],
          }))}
        />
        <span className="text-xs text-gray-500 dark:text-gray-400">Look back</span>
        <Segmented
          size="small"
          value={lookback}
          onChange={(value) => onLookback(value as number)}
          options={[7, 30, 60].map((days) => ({ value: days, label: `${days} days` }))}
        />
        <Tooltip title="Shrinks each plan by the open rightsizing and idle savings on the usage it covers, so they're never counted twice">
          <label className="flex cursor-pointer items-center gap-2 text-sm text-gray-700 dark:text-gray-200">
            <Switch size="small" checked={rightsizingFirst} onChange={onRightsizingFirst} />
            Rightsize first
          </label>
        </Tooltip>
      </div>
      {plan && (
        <>
          <p className="mt-0 mb-3 text-xs text-gray-500 dark:text-gray-400">
            {TYPE_FLEXIBILITY[plan.type]}
            {rightsizingFirst && plan.rightsizingMonthly > 0 && (
              <>
                {' '}
                Reduced by {money(plan.rightsizingMonthly)}/month of open rightsizing (
                {Math.round((1 - plan.factor) * 100)}% of the eligible usage).
              </>
            )}
          </p>
          <div className="grid grid-cols-1 gap-4 xl:grid-cols-2">
            <div className="min-w-0 overflow-x-auto">
              <table className="w-full border-separate border-spacing-1 text-sm">
                <thead>
                  <tr>
                    <th />
                    {payments.map((payment) => (
                      <th
                        key={payment}
                        className="text-xs font-normal text-gray-500 dark:text-gray-400"
                      >
                        {PAYMENT_LABEL[payment]}
                      </th>
                    ))}
                  </tr>
                </thead>
                <tbody>
                  {TERMS.map((term) => (
                    <tr key={term}>
                      <th className="pr-1 text-left text-xs font-medium whitespace-nowrap text-gray-600 dark:text-gray-300">
                        {TERM_LABEL[term]}
                      </th>
                      {payments.map((payment) => (
                        <td key={payment} className="align-top">
                          <OptionCell
                            option={optionAt(plan, term, payment)}
                            selected={
                              option?.term === term && option?.payment === payment && option.offered
                            }
                            money={money}
                            onPick={() => setPicked(`${term}|${payment}`)}
                          />
                        </td>
                      ))}
                    </tr>
                  ))}
                </tbody>
              </table>
              {option?.offered && option.monthlySavings > 0 && (
                <div className="mt-2 flex flex-col gap-2 rounded-md border border-emerald-200 bg-emerald-50 p-3 text-sm text-emerald-900 dark:border-emerald-900 dark:bg-emerald-950 dark:text-emerald-200">
                  <span>
                    Buy{' '}
                    <b>
                      {option.hourlyCommitment > 0
                        ? `${money(option.hourlyCommitment)} an hour`
                        : option.commitment}
                    </b>{' '}
                    ({TYPE_LABEL[plan.type]}, {TERM_LABEL[option.term]},{' '}
                    {PAYMENT_LABEL[option.payment].toLowerCase()}) to save about{' '}
                    <b>{money(option.monthlySavings)}</b> a month
                    {option.savingsPercent > 0 && ` (${option.savingsPercent.toFixed(0)}%)`};{' '}
                    {paybackText(option)}.
                    {option.averageUtilization > 0 &&
                      ` Expected use: ${option.averageUtilization.toFixed(0)}%.`}
                  </span>
                  <span>
                    <Button size="small" onClick={() => setBuying(true)}>
                      How to buy
                    </Button>
                  </span>
                </div>
              )}
            </div>
            <div className="min-w-0">
              {plan.usage.length > 0 ? (
                <UsageChart points={plan.usage} currency={plan.currency || 'USD'} />
              ) : (
                <div className="flex h-[220px] items-center justify-center text-center text-sm text-gray-500 dark:text-gray-400">
                  No daily cost for this usage in the exports or billing API yet
                </div>
              )}
              <p className="m-0 text-xs text-gray-500 dark:text-gray-400">
                Daily cost of the usage {TYPE_SHORT[plan.type]} covers. A coverage slider needs an
                hourly export; until then the provider&apos;s recommended amount is used. Read{' '}
                {new Date(plan.refreshedAt).toLocaleDateString()}.
              </p>
            </div>
          </div>
          {option?.offered && option.lines.length > 0 && (
            <table className="mt-3 w-full text-xs">
              <thead className="text-gray-500 dark:text-gray-400">
                <tr>
                  <th className="py-1 text-left font-normal">What to buy</th>
                  <th className="text-left font-normal">Region</th>
                  <th className="text-left font-normal">Account</th>
                  <th className="text-right font-normal">Saves / month</th>
                  <th className="text-right font-normal">Upfront</th>
                </tr>
              </thead>
              <tbody className="text-gray-800 dark:text-gray-200">
                {option.lines.map((line, index) => (
                  <tr key={index} className="border-t border-gray-100 dark:border-gray-700">
                    <td className="py-1">{line.description}</td>
                    <td>{line.region || '—'}</td>
                    <td className="font-mono">{line.account || '—'}</td>
                    <td className="text-right tabular-nums">{money(line.monthlySavings)}</td>
                    <td className="text-right tabular-nums">
                      {line.upfront ? money(line.upfront) : '—'}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          )}
        </>
      )}
      {plan && option && (
        <Modal
          open={buying}
          onCancel={() => setBuying(false)}
          footer={<Button onClick={() => setBuying(false)}>Close</Button>}
          title={`How to buy: ${TYPE_LABEL[plan.type]}`}
        >
          <ol className="flex list-decimal flex-col gap-1.5 pl-5 text-sm">
            {howToBuy(plan, option).map((step) => (
              <li key={step}>{step}</li>
            ))}
          </ol>
          <p className="mb-0 text-xs text-gray-500 dark:text-gray-400">
            Obzly only reads: purchases happen in the provider&apos;s console. Once bought, the
            commitment shows under Existing commitments after the next weekly refresh.
          </p>
        </Modal>
      )}
    </Panel>
  );
};

const OptionCell = ({
  option,
  selected,
  money,
  onPick,
}: {
  option?: PurchaseOption;
  selected: boolean;
  money: (amount: number) => string;
  onPick: () => void;
}) => {
  if (!option) {
    return (
      <div className="rounded-md border border-dashed border-gray-200 p-2 text-center text-xs text-gray-400 dark:border-gray-700">
        Not read
      </div>
    );
  }
  if (!option.offered) {
    return (
      <Tooltip title={option.reason}>
        <div className="rounded-md border border-dashed border-gray-200 p-2 text-center text-xs text-gray-400 dark:border-gray-700">
          Not offered
        </div>
      </Tooltip>
    );
  }
  if (option.monthlySavings <= 0) {
    return (
      <Tooltip title={option.reason}>
        <div className="rounded-md border border-gray-200 p-2 text-center text-xs text-gray-500 dark:border-gray-700 dark:text-gray-400">
          Nothing to buy
        </div>
      </Tooltip>
    );
  }
  return (
    <button
      type="button"
      onClick={onPick}
      aria-pressed={selected}
      className={`flex w-full cursor-pointer flex-col gap-0.5 rounded-md border p-2 text-left ${selected ? 'border-emerald-600 ring-2 ring-emerald-600/30' : 'border-gray-200 hover:border-gray-400 dark:border-gray-700'}`}
    >
      <span className="flex flex-wrap gap-1">
        {option.labels.includes('lowest_risk') && (
          <Tag color="blue" className="m-0 text-[10px]">
            Lowest risk
          </Tag>
        )}
        {option.labels.includes('most_savings') && (
          <Tag color="green" className="m-0 text-[10px]">
            Most savings
          </Tag>
        )}
      </span>
      <span className="font-semibold text-gray-900 tabular-nums dark:text-gray-100">
        {money(option.monthlySavings)}
        <span className="text-xs font-normal text-gray-500">/mo</span>
      </span>
      <span className="text-xs text-gray-500 dark:text-gray-400">
        {option.savingsPercent > 0 && `${option.savingsPercent.toFixed(0)}% off · `}
        {option.upfront > 0 ? `${money(option.upfront)} upfront` : 'nothing upfront'}
      </span>
      <span className="text-xs text-gray-500 dark:text-gray-400">{paybackText(option)}</span>
    </button>
  );
};

const UsageChart = ({ points, currency }: { points: UsagePoint[]; currency: string }) => {
  const usage = useCallback(
    (colors: ChartColors) => {
      const days = points;
      return {
        ...baseOption(colors),
        grid: { left: 8, right: 8, top: 28, bottom: 8, containLabel: true },
        xAxis: {
          type: 'category' as const,
          data: days.map((point) => point.day.slice(5)),
          axisLabel: { color: colors.text, fontSize: 11, hideOverlap: true },
          axisLine: { lineStyle: { color: colors.axis } },
        },
        yAxis: {
          type: 'value' as const,
          axisLabel: {
            color: colors.text,
            fontSize: 11,
            formatter: (value: number) => formatMoneyCompact(value, currency),
          },
          splitLine: { lineStyle: { color: colors.split } },
        },
        series: [
          {
            name: 'Already committed',
            type: 'bar' as const,
            stack: 'usage',
            itemStyle: { color: '#94a3b8' },
            data: days.map((point) => point.committed),
          },
          {
            name: 'On demand',
            type: 'bar' as const,
            stack: 'usage',
            itemStyle: { color: '#009966' },
            data: days.map((point) => point.onDemand),
          },
        ],
      };
    },
    [points, currency],
  );

  return <EChart option={usage} height={220} />;
};
