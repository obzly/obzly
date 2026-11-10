import { DownloadOutlined, LoadingOutlined } from '@ant-design/icons';
import { Button, DatePicker, Segmented, Select, Spin, Table, Tag, Tooltip } from 'antd';
import type { ColumnsType } from 'antd/es/table';
import dayjs from 'dayjs';
import { useCallback, useEffect, useMemo, useState } from 'react';

import {
  type CostDimension,
  type ExploreResponse,
  type ExploreRow,
  type FacetsResponse,
  costApi,
} from '../../../entity/cost';
import { downloadText, toCSV } from '../../../shared/lib';
import { type ChartColors, EChart, Panel, baseOption } from '../../synthetics';
import {
  BASE_DIMENSIONS,
  type ExplorerState,
  activeDimensions,
  dimensionLabel,
  drillNext,
  exploreCsvRows,
  filterValues,
  scopeLine,
  setFilterValues,
  valueLabel,
} from '../lib/explorer';
import { formatMoney, formatMoneyCompact } from '../lib/format';
import { type CostSite, type SiteOutcome, mergeExplore, mergeFacets } from '../lib/merge';
import {
  GRANULARITY_LABEL,
  RANGE_LABEL,
  type RangePreset,
  allowedGranularities,
  fitGranularity,
  resolveRange,
} from '../lib/ranges';
import { fanOut } from '../lib/sites';
import { FacetFilter } from './FacetFilter';
import { SavedViewsBar } from './SavedViewsBar';
import { CoverageMark, SiteErrors } from './parts';

interface Props {
  sites: CostSite[];
  refresh: number;
  state: ExplorerState;
  onChange: (state: ExplorerState) => void;
  applyDefault: boolean;
}

const STACKED = 8;
const PALETTE = [
  '#009966',
  '#2563eb',
  '#d97706',
  '#7c3aed',
  '#db2777',
  '#0891b2',
  '#65a30d',
  '#dc2626',
];
const OTHER_COLOR = '#9ca3af';

const today = () => new Date().toISOString().slice(0, 10);

export const ExplorerTab = ({ sites, refresh, state, onChange, applyDefault }: Props) => {
  const [result, setResult] = useState<{
    explore: ExploreResponse;
    outcomes: SiteOutcome<unknown>[];
  }>();
  const [facets, setFacets] = useState<FacetsResponse>({ dimensions: {} });
  const [loading, setLoading] = useState(true);
  const [currency, setCurrency] = useState<string>();

  const range = useMemo(() => resolveRange(state.range, today(), state.custom), [state]);
  const request = useMemo(
    () => ({
      ...range,
      granularity: state.granularity,
      groupBy: state.groupBy,
      metric: state.metric,
      filters: state.filters,
    }),
    [range, state],
  );

  useEffect(() => {
    const controller = new AbortController();
    setLoading(true);
    void Promise.all([
      fanOut(sites, (siteId) => costApi.explore(siteId, request, controller.signal)),
      fanOut(sites, (siteId) => costApi.facets(siteId, request, controller.signal)),
    ]).then(([explores, facetOutcomes]) => {
      if (controller.signal.aborted) return;
      setResult({ explore: mergeExplore(explores), outcomes: explores });
      setFacets(mergeFacets(facetOutcomes));
      setLoading(false);
    });
    return () => controller.abort();
  }, [sites, request, refresh]);

  const update = (patch: Partial<ExplorerState>) => onChange({ ...state, ...patch });
  const setRange = (preset: RangePreset, custom = state.custom) => {
    const next = resolveRange(preset, today(), custom);
    update({ range: preset, custom, granularity: fitGranularity(next, state.granularity) });
  };

  const explore = result?.explore;
  const block =
    explore?.currencies.find((candidate) => candidate.currency === currency) ??
    explore?.currencies[0];
  const money = useCallback(
    (amount: number) => formatMoney(amount, block?.currency ?? 'USD'),
    [block],
  );

  const drill = (row: ExploreRow) => {
    const next = drillNext(state.groupBy);
    if (!row.key || !next) return;
    update({
      groupBy: next,
      filters: setFilterValues(state.filters, state.groupBy, [row.key]),
    });
  };

  const tagDimensions = Object.keys(facets.dimensions).filter((key) =>
    key.startsWith('tag:'),
  ) as CostDimension[];
  // A tag filter stays offered after its key leaves the range.
  const filterDimensions: CostDimension[] = [
    ...BASE_DIMENSIONS,
    ...new Set([
      ...tagDimensions,
      ...activeDimensions(state.filters).filter((d) => d.startsWith('tag:')),
    ]),
  ];

  const chart = useCallback(
    (colors: ChartColors) => {
      if (!explore || !block) return {};
      const top = block.rows.slice(0, STACKED);
      const rest = explore.periods.map(
        (_, index) =>
          (block.other?.values[index] ?? 0) +
          block.rows.slice(STACKED).reduce((total, row) => total + (row.values[index] ?? 0), 0),
      );
      const series = [
        ...top.map((row, index) => ({
          name: valueLabel(state.groupBy, row.key, row.label),
          type: 'bar' as const,
          stack: 'cost',
          itemStyle: { color: PALETTE[index % PALETTE.length] },
          data: row.values,
        })),
        ...(rest.some((value) => value !== 0)
          ? [
              {
                name: 'Other',
                type: 'bar' as const,
                stack: 'cost',
                itemStyle: { color: OTHER_COLOR },
                data: rest,
              },
            ]
          : []),
      ];
      return {
        ...baseOption(colors),
        grid: { left: 8, right: 16, top: 36, bottom: 8, containLabel: true },
        tooltip: {
          ...baseOption(colors).tooltip,
          trigger: 'axis' as const,
          axisPointer: { type: 'shadow' as const },
          valueFormatter: (value: unknown) => money(Number(value)),
        },
        xAxis: {
          type: 'category' as const,
          data: explore.periods.map((period) => period.key),
          axisLabel: { color: colors.text, fontSize: 11, hideOverlap: true },
          axisLine: { lineStyle: { color: colors.axis } },
        },
        yAxis: {
          type: 'value' as const,
          axisLabel: {
            color: colors.text,
            fontSize: 11,
            formatter: (value: number) => formatMoneyCompact(value, block.currency),
          },
          splitLine: { lineStyle: { color: colors.split } },
        },
        series,
      };
    },
    [explore, block, state.groupBy, money],
  );

  const onChartClick = useCallback(
    (params: { seriesIndex?: number }) => {
      const row = block?.rows[params.seriesIndex ?? -1];
      if (row && (params.seriesIndex ?? STACKED) < STACKED) drill(row);
    },
    // drill reads the latest state through the props it closes over.
    [block, state],
  );

  const rows = block ? [...block.rows, ...(block.other ? [block.other] : [])] : [];
  const columns: ColumnsType<ExploreRow> = [
    {
      title: dimensionLabel(state.groupBy),
      key: 'group',
      fixed: 'left',
      width: 280,
      ellipsis: true,
      render: (_, row) =>
        row.key ? (
          valueLabel(state.groupBy, row.key, row.label)
        ) : (
          <span className="text-gray-500 dark:text-gray-400">{row.label || '(none)'}</span>
        ),
    },
    ...(explore?.periods ?? []).map((period, index) => ({
      title: (
        <span className="whitespace-nowrap">
          {period.key}
          <CoverageMark coverage={period.coverage} />
        </span>
      ),
      key: period.key,
      align: 'right' as const,
      render: (_: unknown, row: ExploreRow) => (
        <Amount
          value={row.values[index] ?? 0}
          missing={period.coverage === 'none'}
          format={money}
        />
      ),
    })),
    {
      title: 'Total',
      key: 'total',
      align: 'right',
      fixed: 'right',
      render: (_, row) => <span className="font-semibold tabular-nums">{money(row.total)}</span>,
    },
  ];

  const groupOptions = [
    ...BASE_DIMENSIONS.map((dimension) => ({ value: dimension, label: dimensionLabel(dimension) })),
    ...tagDimensions.map((dimension) => ({ value: dimension, label: dimensionLabel(dimension) })),
  ];
  const resourceFromApi =
    state.groupBy === 'resource' || filterValues(state.filters, 'resource').length > 0;

  return (
    <div className="flex flex-col gap-3">
      <div className="flex flex-wrap items-center gap-2">
        <Select
          size="small"
          className="w-40"
          value={state.range}
          onChange={(preset: RangePreset) => setRange(preset)}
          options={(Object.keys(RANGE_LABEL) as RangePreset[]).map((preset) => ({
            value: preset,
            label: RANGE_LABEL[preset],
          }))}
        />
        {state.range === 'custom' && (
          <DatePicker.RangePicker
            size="small"
            allowClear={false}
            value={[dayjs(range.from), dayjs(range.to)]}
            disabledDate={(day) => day.isAfter(dayjs())}
            onChange={(values) => {
              if (values?.[0] && values[1]) {
                setRange('custom', {
                  from: values[0].format('YYYY-MM-DD'),
                  to: values[1].format('YYYY-MM-DD'),
                });
              }
            }}
          />
        )}
        <Segmented
          size="small"
          value={state.granularity}
          onChange={(value) => update({ granularity: value as ExplorerState['granularity'] })}
          options={(['daily', 'monthly', 'quarterly', 'yearly'] as const).map((granularity) => ({
            value: granularity,
            label: GRANULARITY_LABEL[granularity],
            disabled: !allowedGranularities(range).includes(granularity),
          }))}
        />
        <span className="ml-2 text-xs text-gray-500 dark:text-gray-400">Group by</span>
        <Select
          size="small"
          className="w-40"
          value={state.groupBy}
          onChange={(groupBy: CostDimension) => update({ groupBy })}
          options={groupOptions}
        />
        <Segmented
          size="small"
          value={state.metric}
          onChange={(value) => update({ metric: value as ExplorerState['metric'] })}
          options={[
            { value: 'effective', label: 'Effective' },
            { value: 'billed', label: 'Billed' },
          ]}
        />
        <div className="flex-1" />
        <SavedViewsBar<ExplorerState>
          kind="cost"
          state={state}
          applyDefault={applyDefault}
          onOpen={(saved) => onChange({ ...state, ...saved })}
        />
      </div>

      <div className="flex flex-wrap items-center gap-2">
        <span className="text-xs text-gray-500 dark:text-gray-400">Filters</span>
        {filterDimensions.map((dimension) => (
          <FacetFilter
            key={dimension}
            dimension={dimension}
            values={facets.dimensions[dimension] ?? []}
            selected={filterValues(state.filters, dimension)}
            scope={scopeLine(state.filters, dimension)}
            onChange={(values) =>
              update({ filters: setFilterValues(state.filters, dimension, values) })
            }
          />
        ))}
        {activeDimensions(state.filters).length > 0 && (
          <Button size="small" type="link" onClick={() => update({ filters: {} })}>
            Clear all
          </Button>
        )}
      </div>

      {result && <SiteErrors outcomes={result.outcomes} />}

      {!result ? (
        <div className="flex justify-center py-16">
          <Spin indicator={<LoadingOutlined spin />} />
        </div>
      ) : (
        <Spin spinning={loading} indicator={<LoadingOutlined spin />}>
          <div className="flex flex-col gap-3">
            {(explore?.currencies.length ?? 0) > 1 && (
              <div className="flex items-center gap-2 text-sm text-gray-600 dark:text-gray-300">
                Currency
                <Segmented
                  size="small"
                  value={block?.currency}
                  onChange={(value) => setCurrency(value as string)}
                  options={explore!.currencies.map((item) => ({
                    value: item.currency,
                    label: `${item.currency} ${formatMoneyCompact(item.total, item.currency)}`,
                  }))}
                />
              </div>
            )}
            <Panel
              title={
                block ? (
                  <span>
                    {money(block.total)}{' '}
                    <span className="font-normal text-gray-500 dark:text-gray-400">
                      {state.metric === 'effective' ? 'effective' : 'billed'}, {range.from} to{' '}
                      {range.to}
                    </span>
                  </span>
                ) : (
                  'No cost in this range'
                )
              }
              subtitle={
                drillNext(state.groupBy)
                  ? `Click a bar or row to see its ${dimensionLabel(drillNext(state.groupBy)!).toLowerCase()}s.`
                  : undefined
              }
              extra={
                block && (
                  <Button
                    size="small"
                    icon={<DownloadOutlined />}
                    onClick={() =>
                      downloadText(
                        `cost-${state.groupBy.replace(':', '-')}-${range.from}-${range.to}-${block.currency}.csv`,
                        toCSV(exploreCsvRows(explore!, block.currency, state.groupBy)),
                        'text/csv',
                      )
                    }
                  >
                    CSV
                  </Button>
                )
              }
            >
              {block && <EChart option={chart} height={300} onClick={onChartClick} />}
              {resourceFromApi && (
                <Tag color="blue" className="mt-2">
                  Single resources come from FOCUS exports only; connections on the billing API
                  aren&apos;t in this view.
                </Tag>
              )}
            </Panel>
            {block && (
              <Table<ExploreRow>
                size="small"
                rowKey={(row) => row.key || '__other'}
                dataSource={rows}
                columns={columns}
                pagination={rows.length > 50 ? { pageSize: 50, showSizeChanger: false } : false}
                scroll={{ x: 'max-content' }}
                onRow={(row) => ({
                  onClick: () => drill(row),
                  className: row.key && drillNext(state.groupBy) ? 'cursor-pointer' : '',
                })}
                summary={() => (
                  <Table.Summary.Row>
                    <Table.Summary.Cell index={0} className="font-semibold">
                      Total
                    </Table.Summary.Cell>
                    {block.totals.map((total, index) => (
                      <Table.Summary.Cell key={index} index={index + 1} align="right">
                        <Amount
                          value={total}
                          missing={explore!.periods[index]?.coverage === 'none'}
                          format={money}
                          strong
                        />
                      </Table.Summary.Cell>
                    ))}
                    <Table.Summary.Cell index={block.totals.length + 1} align="right">
                      <span className="font-semibold tabular-nums">{money(block.total)}</span>
                    </Table.Summary.Cell>
                  </Table.Summary.Row>
                )}
              />
            )}
          </div>
        </Spin>
      )}
    </div>
  );
};

/** A period no connection has data for reads n/a, never a zero. */
const Amount = ({
  value,
  missing,
  format,
  strong,
}: {
  value: number;
  missing: boolean;
  format: (amount: number) => string;
  strong?: boolean;
}) =>
  missing && value === 0 ? (
    <Tooltip title="No connection in scope has data for this period">
      <span className="text-gray-400 dark:text-gray-500">n/a</span>
    </Tooltip>
  ) : (
    <span className={`tabular-nums ${strong ? 'font-semibold' : ''}`}>{format(value)}</span>
  );
