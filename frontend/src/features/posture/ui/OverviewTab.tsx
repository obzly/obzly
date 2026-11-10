import { LoadingOutlined } from '@ant-design/icons';
import { Empty, Progress, Spin, Tooltip } from 'antd';
import dayjs from 'dayjs';
import { useCallback, useEffect, useState } from 'react';

import { type PostureSummary, type Severity, postureApi } from '../../../entity/posture';
import { type CostSite, SiteErrors, type SiteOutcome, Tile, fanOut } from '../../cost';
import { type ChartColors, EChart, Panel } from '../../synthetics';
import {
  SEVERITIES,
  SEVERITY_COLOR,
  SEVERITY_LABEL,
  mergeSummary,
  sourcesLabel,
} from '../lib/merge';
import { CoverageTag } from './parts';

interface Props {
  sites: CostSite[];
  refresh: number;
  onFindings: (filter: { severity?: Severity; standard?: string }) => void;
  onCoverage: () => void;
  onManageCloud?: () => void;
}

export const OverviewTab = ({ sites, refresh, onFindings, onCoverage, onManageCloud }: Props) => {
  const [data, setData] = useState<{ summary: PostureSummary; outcomes: SiteOutcome<unknown>[] }>();

  useEffect(() => {
    const controller = new AbortController();
    void fanOut(sites, (siteId) => postureApi.summary(siteId, controller.signal)).then(
      (outcomes) => {
        if (!controller.signal.aborted) setData({ summary: mergeSummary(outcomes), outcomes });
      },
    );
    return () => controller.abort();
  }, [sites, refresh]);

  if (!data) {
    return (
      <div className="flex justify-center py-16">
        <Spin indicator={<LoadingOutlined spin />} />
      </div>
    );
  }
  const { summary } = data;
  const coverage = summary.coverage;
  const scopes =
    coverage.native + coverage.fallback + coverage.baseline + coverage.notCovered + coverage.stale;
  if (summary.standards.length === 0 && scopes === 0) {
    return (
      <>
        <SiteErrors outcomes={data.outcomes} />
        <Empty
          className="py-12"
          description="No posture data yet. Turn on Security posture for a cloud connection; the probe reads it within minutes."
        >
          {onManageCloud && (
            <button
              type="button"
              className="cursor-pointer rounded bg-emerald-600 px-3 py-1.5 text-white hover:bg-emerald-700"
              onClick={onManageCloud}
            >
              Cloud connections
            </button>
          )}
        </Empty>
      </>
    );
  }

  return (
    <div className="flex flex-col gap-4">
      <SiteErrors outcomes={data.outcomes} />
      <div className="grid grid-cols-2 gap-4 md:grid-cols-3 xl:grid-cols-6">
        {SEVERITIES.slice(0, 4).map((severity) => (
          <button
            key={severity}
            type="button"
            className="cursor-pointer text-left"
            onClick={() => onFindings({ severity })}
          >
            <Tile
              label={`${SEVERITY_LABEL[severity]} findings`}
              value={
                <span style={{ color: SEVERITY_COLOR[severity] }}>
                  {summary.open[severity] ?? 0}
                </span>
              }
            />
          </button>
        ))}
        <Tile label="Suppressed" value={summary.suppressed} hint="Left out of scores and alerts" />
        <Tile label="Resolved" value={summary.resolved30d} hint="Last 30 days" />
      </div>

      <Panel
        title="Standards"
        subtitle="Controls passed of those evaluated; a control nothing evaluated isn't counted as passed"
      >
        {summary.standards.length === 0 ? (
          <div className="py-6 text-center text-sm text-gray-500 dark:text-gray-400">
            No standard evaluated yet
          </div>
        ) : (
          <div className="grid grid-cols-1 gap-3 md:grid-cols-2 xl:grid-cols-3">
            {summary.standards.map((standard) => (
              <button
                key={standard.standard}
                type="button"
                onClick={() => onFindings({ standard: standard.standard })}
                className="flex cursor-pointer flex-col gap-2 rounded-lg border border-gray-200 p-3 text-left hover:border-gray-400 dark:border-gray-700"
              >
                <span className="flex items-start justify-between gap-2">
                  <span className="text-sm font-medium text-gray-900 dark:text-gray-100">
                    {standard.name}
                  </span>
                  <Tooltip title="Where this standard's results come from">
                    <span className="shrink-0 rounded bg-gray-100 px-1.5 py-px text-[11px] text-gray-600 dark:bg-gray-700 dark:text-gray-300">
                      {sourcesLabel(standard.sources)}
                    </span>
                  </Tooltip>
                </span>
                <span className="flex items-center gap-3">
                  <Progress
                    type="circle"
                    size={56}
                    percent={standard.percent}
                    strokeColor={
                      standard.percent >= 80
                        ? '#009966'
                        : standard.percent >= 50
                          ? '#f59e0b'
                          : '#dc2626'
                    }
                    format={(value) => `${Math.round(value ?? 0)}%`}
                  />
                  <span className="text-xs text-gray-500 dark:text-gray-400">
                    {standard.passed} of {standard.evaluated} controls passed
                    <br />
                    {standard.failed} failing
                  </span>
                  <span className="min-w-0 flex-1">
                    <Sparkline points={standard.trend ?? []} />
                  </span>
                </span>
              </button>
            ))}
          </div>
        )}
      </Panel>

      <Panel
        title="Coverage"
        subtitle={`Scopes (account or subscription and region) by where their findings come from${summary.lastScanAt ? `; last read ${dayjs(summary.lastScanAt).fromNow()}` : ''}`}
        extra={
          <button
            type="button"
            className="cursor-pointer text-sm text-emerald-600"
            onClick={onCoverage}
          >
            Details
          </button>
        }
      >
        <div className="flex flex-wrap items-center gap-4 text-sm">
          {(
            [
              ['native', coverage.native],
              ['fallback', coverage.fallback],
              ['baseline', coverage.baseline],
              ['not_covered', coverage.notCovered],
              ['stale', coverage.stale],
            ] as const
          )
            .filter(([, count]) => count > 0)
            .map(([status, count]) => (
              <span key={status} className="inline-flex items-center gap-2">
                <CoverageTag status={status} />
                <span className="tabular-nums">{count}</span>
              </span>
            ))}
        </div>
      </Panel>
    </div>
  );
};

const Sparkline = ({ points }: { points: { day: string; percent: number }[] }) => {
  const option = useCallback(
    (colors: ChartColors) => ({
      animation: false,
      grid: { left: 0, right: 0, top: 4, bottom: 4 },
      tooltip: {
        trigger: 'axis' as const,
        backgroundColor: colors.tooltip,
        borderColor: colors.axis,
        textStyle: { color: colors.text, fontSize: 12 },
        valueFormatter: (value: unknown) => `${value}%`,
      },
      xAxis: { type: 'category' as const, show: false, data: points.map((point) => point.day) },
      yAxis: { type: 'value' as const, show: false, min: 0, max: 100 },
      series: [
        {
          type: 'line' as const,
          data: points.map((point) => point.percent),
          showSymbol: points.length < 2,
          lineStyle: { color: '#009966', width: 2 },
          itemStyle: { color: '#009966' },
        },
      ],
    }),
    [points],
  );
  if (points.length === 0) return null;
  return <EChart option={option} height={44} />;
};
