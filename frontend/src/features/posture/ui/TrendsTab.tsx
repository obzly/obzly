import { LoadingOutlined } from '@ant-design/icons';
import { DatePicker, Segmented, Select, Spin } from 'antd';
import dayjs from 'dayjs';
import { useCallback, useEffect, useMemo, useState } from 'react';

import type { Granularity } from '../../../entity/cost';
import {
  type PostureFacets,
  type PostureFilters,
  type Severity,
  type TrendsResponse,
  postureApi,
} from '../../../entity/posture';
import {
  type CostSite,
  GRANULARITY_LABEL,
  RANGE_LABEL,
  type RangePreset,
  SavedViewsBar,
  SiteErrors,
  type SiteOutcome,
  allowedGranularities,
  fanOut,
  fitGranularity,
  resolveRange,
} from '../../cost';
import { type ChartColors, EChart, Panel, baseOption } from '../../synthetics';
import { type TrendsState } from '../lib/format';
import { SEVERITIES, SEVERITY_COLOR, SEVERITY_LABEL, mergeFacets, mergeTrends } from '../lib/merge';

interface Props {
  sites: CostSite[];
  refresh: number;
  state: TrendsState;
  onChange: (state: TrendsState) => void;
}

const LINE_COLORS = ['#009966', '#2563eb', '#d97706', '#7c3aed', '#db2777'];
const today = () => new Date().toISOString().slice(0, 10);

export const TrendsTab = ({ sites, refresh, state, onChange }: Props) => {
  const [data, setData] = useState<{ trends: TrendsResponse; outcomes: SiteOutcome<unknown>[] }>();
  const [facets, setFacets] = useState<PostureFacets>({ dimensions: {} });
  const [loading, setLoading] = useState(true);
  const range = useMemo(() => resolveRange(state.range, today(), state.custom), [state]);

  useEffect(() => {
    const controller = new AbortController();
    setLoading(true);
    const request = { ...range, granularity: state.granularity, filters: state.filters };
    void Promise.all([
      fanOut(sites, (siteId) => postureApi.trends(siteId, request, controller.signal)),
      fanOut(sites, (siteId) =>
        postureApi.facets(siteId, { ...state.filters, status: 'all' }, controller.signal),
      ),
    ]).then(([outcomes, facetOutcomes]) => {
      if (controller.signal.aborted) return;
      setData({ trends: mergeTrends(outcomes), outcomes });
      setFacets(mergeFacets(facetOutcomes));
      setLoading(false);
    });
    return () => controller.abort();
  }, [sites, refresh, range, state.granularity, state.filters]);

  const update = (patch: Partial<TrendsState>) => onChange({ ...state, ...patch });
  const setRange = (preset: RangePreset, custom = state.custom) => {
    const next = resolveRange(preset, today(), custom);
    update({ range: preset, custom, granularity: fitGranularity(next, state.granularity) });
  };
  const setFilter = (key: keyof PostureFilters, values: string[]) =>
    update({ filters: { ...state.filters, [key]: values.length ? values : undefined } });

  const periods = data?.trends.periods ?? [];
  const axis = (colors: ChartColors) => ({
    type: 'category' as const,
    data: periods.map((period) => period.key),
    axisLabel: { color: colors.text, fontSize: 11, hideOverlap: true },
    axisLine: { lineStyle: { color: colors.axis } },
  });
  const value = (colors: ChartColors, suffix = '') => ({
    type: 'value' as const,
    axisLabel: { color: colors.text, fontSize: 11, formatter: `{value}${suffix}` },
    splitLine: { lineStyle: { color: colors.split } },
  });

  const openChart = useCallback(
    (colors: ChartColors) => ({
      ...baseOption(colors),
      grid: { left: 8, right: 16, top: 36, bottom: 8, containLabel: true },
      tooltip: {
        ...baseOption(colors).tooltip,
        trigger: 'axis' as const,
        axisPointer: { type: 'shadow' as const },
      },
      xAxis: axis(colors),
      yAxis: value(colors),
      series: SEVERITIES.map((severity: Severity) => ({
        name: SEVERITY_LABEL[severity],
        type: 'bar' as const,
        stack: 'open',
        itemStyle: { color: SEVERITY_COLOR[severity] },
        data: periods.map((period) => period.open[severity] ?? 0),
      })),
    }),
    [periods],
  );

  const flowChart = useCallback(
    (colors: ChartColors) => ({
      ...baseOption(colors),
      grid: { left: 8, right: 16, top: 36, bottom: 8, containLabel: true },
      xAxis: axis(colors),
      yAxis: value(colors),
      series: [
        {
          name: 'New or reopened',
          type: 'line' as const,
          data: periods.map((period) => period.opened),
          itemStyle: { color: '#dc2626' },
        },
        {
          name: 'Resolved',
          type: 'line' as const,
          data: periods.map((period) => period.resolved),
          itemStyle: { color: '#009966' },
        },
      ],
    }),
    [periods],
  );

  const standards = useMemo(
    () => [...new Set(periods.flatMap((period) => Object.keys(period.scores)))].sort(),
    [periods],
  );
  const names = useMemo(
    () =>
      Object.fromEntries(
        (facets.dimensions.standard ?? []).map((item) => [item.value, item.label || item.value]),
      ),
    [facets],
  );
  const scoreChart = useCallback(
    (colors: ChartColors) => ({
      ...baseOption(colors),
      grid: { left: 8, right: 16, top: 36, bottom: 8, containLabel: true },
      tooltip: {
        ...baseOption(colors).tooltip,
        trigger: 'axis' as const,
        valueFormatter: (value: unknown) =>
          value === null || value === undefined ? 'n/a' : `${value}%`,
      },
      xAxis: axis(colors),
      yAxis: { ...value(colors, '%'), min: 0, max: 100 },
      series: standards.map((standard, index) => ({
        name: names[standard] ?? standard,
        type: 'line' as const,
        connectNulls: false,
        itemStyle: { color: LINE_COLORS[index % LINE_COLORS.length] },
        // A period before the standard was evaluated is a gap, not a zero.
        data: periods.map((period) => period.scores[standard] ?? null),
      })),
    }),
    [periods, standards, names],
  );

  const facetOptions = (key: string, labels: Record<string, string> = {}) =>
    (facets.dimensions[key] ?? []).map((item) => ({
      value: item.value,
      label: `${item.label || labels[item.value] || item.value} (${item.count})`,
    }));

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
          onChange={(next) => update({ granularity: next as Granularity })}
          options={(['daily', 'monthly', 'quarterly', 'yearly'] as const).map((granularity) => ({
            value: granularity,
            label: GRANULARITY_LABEL[granularity],
            disabled: !allowedGranularities(range).includes(granularity),
          }))}
        />
        <div className="flex-1" />
        <SavedViewsBar<TrendsState>
          kind="posture"
          state={state}
          onOpen={(saved) => onChange({ ...state, ...saved })}
        />
      </div>
      <div className="flex flex-wrap items-center gap-2">
        <span className="text-xs text-gray-500 dark:text-gray-400">Filters</span>
        {(
          [
            ['severity', 'Severity', SEVERITY_LABEL],
            ['standard', 'Standard', {}],
            ['provider', 'Provider', { aws: 'AWS', azure: 'Azure' }],
            ['source', 'Source', { native: 'Native', baseline: 'Obzly baseline' }],
            ['account', 'Account', {}],
          ] as const
        ).map(([key, label, labels]) => (
          <Select
            key={key}
            size="small"
            mode="multiple"
            allowClear
            maxTagCount={1}
            className="min-w-36"
            placeholder={label}
            value={(state.filters[key] as string[] | undefined) ?? []}
            onChange={(values: string[]) => setFilter(key, values)}
            options={facetOptions(key, labels as Record<string, string>)}
          />
        ))}
      </div>
      {data && <SiteErrors outcomes={data.outcomes} />}
      {!data ? (
        <div className="flex justify-center py-16">
          <Spin indicator={<LoadingOutlined spin />} />
        </div>
      ) : (
        <Spin spinning={loading} indicator={<LoadingOutlined spin />}>
          <div className="grid grid-cols-1 gap-4 xl:grid-cols-2">
            <Panel
              title="Open findings"
              subtitle="At the end of each period, by severity, suppressed ones included"
            >
              <EChart option={openChart} height={260} />
            </Panel>
            <Panel title="New and resolved" subtitle="Within each period">
              <EChart option={flowChart} height={260} />
            </Panel>
            <Panel
              title="Standard scores"
              subtitle="At the end of each period; gaps are periods before a standard was evaluated"
              className="xl:col-span-2"
            >
              <EChart option={scoreChart} height={260} />
            </Panel>
          </div>
        </Spin>
      )}
    </div>
  );
};
