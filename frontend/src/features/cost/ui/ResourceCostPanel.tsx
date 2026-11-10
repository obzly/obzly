// One resource's cost in the Infrastructure asset drawer, matched by its cloud ID, with its open
// savings recommendations.
import { LoadingOutlined } from '@ant-design/icons';
import { Spin } from 'antd';
import { useCallback, useEffect, useState } from 'react';

import { type CostRecommendation, type ResourceCostResponse, costApi } from '../../../entity/cost';
import { type ChartColors, EChart, Panel } from '../../synthetics';
import { formatMoney } from '../lib/format';

interface Props {
  siteId: string;
  /** The ARN or Azure resource ID. */
  cloudId: string;
}

const DAYS = 30;

const CHARGE_LABEL: Record<string, string> = {
  Usage: 'Usage',
  Purchase: 'Purchases',
  Tax: 'Tax',
  Credit: 'Credits',
  Adjustment: 'Adjustments',
};

export const ResourceCostPanel = ({ siteId, cloudId }: Props) => {
  const [data, setData] = useState<ResourceCostResponse>();
  const [error, setError] = useState<string>();
  const [recommendations, setRecommendations] = useState<CostRecommendation[]>([]);

  useEffect(() => {
    const controller = new AbortController();
    setRecommendations([]);
    costApi
      .resourceRecommendations(siteId, cloudId, controller.signal)
      .then(setRecommendations)
      // An older Site has no recommendations; the cost above still shows.
      .catch(() => undefined);
    return () => controller.abort();
  }, [siteId, cloudId]);

  useEffect(() => {
    const controller = new AbortController();
    setData(undefined);
    setError(undefined);
    costApi
      .resource(siteId, cloudId, DAYS, controller.signal)
      .then(setData)
      .catch((reason: Error) => {
        if (!controller.signal.aborted) setError(reason.message);
      });
    return () => controller.abort();
  }, [siteId, cloudId]);

  const block = data?.currencies[0];
  const spark = useCallback(
    (colors: ChartColors) => ({
      animation: false,
      grid: { left: 0, right: 0, top: 4, bottom: 0 },
      tooltip: {
        trigger: 'axis' as const,
        backgroundColor: colors.tooltip,
        borderColor: colors.axis,
        textStyle: { color: colors.text, fontSize: 12 },
        valueFormatter: (value: unknown) => formatMoney(Number(value), block?.currency ?? 'USD'),
      },
      xAxis: {
        type: 'category' as const,
        show: false,
        data: block?.days.map((day) => day.day) ?? [],
      },
      yAxis: { type: 'value' as const, show: false },
      series: [
        {
          type: 'bar' as const,
          data: (block?.days ?? []).map((day) => ({
            value: day.amount,
            itemStyle: { color: '#009966', opacity: day.estimated ? 0.45 : 1 },
          })),
        },
      ],
    }),
    [block],
  );

  return (
    <Panel title="Cost" subtitle={`Effective cost, last ${DAYS} days, from FOCUS exports`}>
      {error ? (
        <div className="text-xs text-gray-500 dark:text-gray-400">Cost unavailable: {error}</div>
      ) : !data ? (
        <div className="flex justify-center py-4">
          <Spin indicator={<LoadingOutlined spin />} />
        </div>
      ) : !block ? (
        <div className="text-xs text-gray-500 dark:text-gray-400">
          {data.covered
            ? 'No cost for this resource in the last 30 days.'
            : 'Single-resource cost needs a FOCUS export on the cloud connection; the billing API only splits by account, service and region.'}
        </div>
      ) : (
        <div className="flex flex-col gap-2">
          {data.currencies.map((currency) => (
            <div
              key={currency.currency}
              className="text-xl font-semibold text-gray-900 tabular-nums dark:text-gray-100"
            >
              {formatMoney(currency.total, currency.currency)}
            </div>
          ))}
          <EChart option={spark} height={56} />
          <dl className="m-0 grid grid-cols-[auto_minmax(0,1fr)] gap-x-3 gap-y-0.5 text-xs">
            {Object.entries(block.byCharge)
              .sort(([, a], [, b]) => Math.abs(b) - Math.abs(a))
              .map(([charge, amount]) => (
                <div key={charge} className="contents">
                  <dt className="text-gray-500 dark:text-gray-400">
                    {CHARGE_LABEL[charge] ?? charge}
                  </dt>
                  <dd className="m-0 text-right text-gray-800 tabular-nums dark:text-gray-200">
                    {formatMoney(amount, block.currency)}
                  </dd>
                </div>
              ))}
          </dl>
          {!data.covered && (
            <div className="text-xs text-amber-600 dark:text-amber-400">
              Some days aren&apos;t covered by an export yet.
            </div>
          )}
        </div>
      )}
      {recommendations.length > 0 && (
        <div className="mt-3 flex flex-col gap-1 border-t border-gray-100 pt-2 dark:border-gray-700">
          <div className="text-xs font-medium text-gray-700 dark:text-gray-200">
            Recommendations (Cost → Recommendations)
          </div>
          {recommendations.map((item) => (
            <div key={item.key} className="flex justify-between gap-2 text-xs">
              <span className="text-gray-700 dark:text-gray-300">{item.action}</span>
              <span className="shrink-0 font-medium text-emerald-700 tabular-nums dark:text-emerald-400">
                {item.monthlySavings === null
                  ? 'n/a'
                  : `${formatMoney(item.monthlySavings, item.currency)}/mo`}
              </span>
            </div>
          ))}
        </div>
      )}
    </Panel>
  );
};
