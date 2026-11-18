import { InfoCircleOutlined } from '@ant-design/icons';
import { Button, Empty, Tooltip } from 'antd';
import dayjs from 'dayjs';
import { useCallback, useEffect, useState } from 'react';

import {
  type FindingStatusFilter,
  type PostureSummary,
  type Severity,
  postureApi,
} from '../../../entity/posture';
import { Card, KpiTile } from '../../../shared/ui/v2';
import {
  type CostSite,
  IconLabel,
  Loading,
  SiteErrors,
  type SiteOutcome,
  StandardMark,
  TileRow,
  fanOut,
} from '../../cost';
import { type ChartColors, EChart } from '../../synthetics';
import {
  SEVERITIES,
  SEVERITY_COLOR,
  SEVERITY_LABEL,
  mergeSummary,
  sourcesLabel,
} from '../lib/merge';
import { CoverageTag, ScoreRing } from './parts';

interface Props {
  sites: CostSite[];
  refresh: number;
  onFindings: (filter: {
    severity?: Severity;
    standard?: string;
    status?: FindingStatusFilter;
  }) => void;
  onCoverage: () => void;
  onManageCloud?: () => void;
}

export const OverviewTab = ({ sites, refresh, onFindings, onCoverage, onManageCloud }: Props) => {
  const [data, setData] = useState<{ summary: PostureSummary; outcomes: SiteOutcome<unknown>[] }>();
  // The standard whose source explanation is open (on click, so it works without hover).
  const [reasonOpen, setReasonOpen] = useState<string | null>(null);

  useEffect(() => {
    const controller = new AbortController();
    void fanOut(sites, (siteId) => postureApi.summary(siteId, controller.signal)).then(
      (outcomes) => {
        if (!controller.signal.aborted) setData({ summary: mergeSummary(outcomes), outcomes });
      },
    );
    return () => controller.abort();
  }, [sites, refresh]);

  if (!data) return <Loading />;
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
            <Button type="primary" onClick={onManageCloud}>
              Cloud connections
            </Button>
          )}
        </Empty>
      </>
    );
  }

  return (
    <div className="flex flex-col gap-4">
      <SiteErrors outcomes={data.outcomes} />
      <TileRow label="Summary">
        {SEVERITIES.slice(0, 4).map((severity) => (
          <KpiTile
            key={severity}
            label={`${SEVERITY_LABEL[severity]} findings`}
            value={
              <span style={{ color: SEVERITY_COLOR[severity] }}>{summary.open[severity] ?? 0}</span>
            }
            note="Open · click to see them"
            onClick={() => onFindings({ severity })}
          />
        ))}
        <KpiTile
          label="Suppressed"
          value={summary.suppressed}
          note="Left out of scores and alerts"
          onClick={() => onFindings({ status: 'suppressed' })}
        />
        <KpiTile
          label="Resolved"
          value={<span className="text-[var(--v2-accent)]">{summary.resolved30d}</span>}
          note="Last 30 days"
          onClick={() => onFindings({ status: 'resolved' })}
        />
      </TileRow>

      <Card
        title="Standards"
        subtitle="Controls passed of those evaluated; a control nothing evaluated isn't counted as passed. Click a standard to see its findings."
      >
        {summary.standards.length === 0 ? (
          <div className="py-6 text-center text-sm text-gray-500 dark:text-gray-400">
            No standard evaluated yet
          </div>
        ) : (
          <div className="grid grid-cols-1 gap-3 md:grid-cols-2 xl:grid-cols-3">
            {summary.standards.map((standard) => (
              <div
                key={standard.standard}
                className="flex flex-col gap-2 rounded-xl border border-gray-200 bg-gray-50 p-4 dark:border-gray-700 dark:bg-gray-900/40"
              >
                <span className="flex items-start justify-between gap-2">
                  <button
                    type="button"
                    onClick={() => onFindings({ standard: standard.standard })}
                    className="hover:text-accent-700 dark:hover:text-accent-400 min-w-0 cursor-pointer text-left text-sm font-semibold text-gray-900 dark:text-gray-50"
                  >
                    <IconLabel
                      icon={<StandardMark standard={standard.standard} name={standard.name} />}
                    >
                      {standard.name}
                    </IconLabel>
                  </button>
                  <Tooltip title="Where this standard's results come from">
                    {standard.reason ? (
                      <button
                        type="button"
                        aria-expanded={reasonOpen === standard.standard}
                        aria-label={`Why ${sourcesLabel(standard.sources)}`}
                        onClick={() =>
                          setReasonOpen((open) =>
                            open === standard.standard ? null : standard.standard,
                          )
                        }
                        className="shrink-0 cursor-pointer rounded-full border-0 bg-gray-100 px-2 py-0.5 text-[11px] text-gray-600 hover:bg-gray-200 dark:bg-gray-700 dark:text-gray-300 dark:hover:bg-gray-600"
                      >
                        {sourcesLabel(standard.sources)} <InfoCircleOutlined />
                      </button>
                    ) : (
                      <span className="shrink-0 rounded-full bg-gray-100 px-2 py-0.5 text-[11px] text-gray-600 dark:bg-gray-700 dark:text-gray-300">
                        {sourcesLabel(standard.sources)}
                      </span>
                    )}
                  </Tooltip>
                </span>
                {reasonOpen === standard.standard && standard.reason && (
                  <span className="text-xs text-gray-500 dark:text-gray-400">
                    {standard.reason}
                  </span>
                )}
                <button
                  type="button"
                  onClick={() => onFindings({ standard: standard.standard })}
                  className="flex cursor-pointer items-center gap-3 text-left"
                >
                  <ScoreRing
                    name={standard.name}
                    passed={standard.passed}
                    evaluated={standard.evaluated}
                    percent={standard.percent}
                  />
                  <span className="text-xs text-gray-500 dark:text-gray-400">
                    {standard.passed} of {standard.evaluated} controls passed
                    <br />
                    {standard.failed} failing
                  </span>
                </button>
                <ScoreTrend points={standard.trend ?? []} />
              </div>
            ))}
          </div>
        )}
      </Card>

      <Card
        title="Coverage"
        subtitle={`Scopes (account or subscription and region) by where their findings come from${summary.lastScanAt ? `; last read ${dayjs(summary.lastScanAt).fromNow()}` : ''}`}
        extra={
          <Button size="small" type="link" onClick={onCoverage}>
            Details
          </Button>
        }
      >
        <div className="flex flex-wrap items-center gap-2 text-sm">
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
              <span
                key={status}
                className="inline-flex items-center gap-2 rounded-lg border border-gray-200 bg-gray-50 px-2.5 py-1 dark:border-gray-700 dark:bg-gray-900/40"
              >
                <CoverageTag status={status} />
                <strong className="font-semibold text-gray-900 tabular-nums dark:text-gray-50">
                  {count}
                </strong>
              </span>
            ))}
        </div>
      </Card>
    </div>
  );
};

/** A standard's score over time; hover a point for its day. */
const ScoreTrend = ({ points }: { points: { day: string; percent: number }[] }) => {
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
          lineStyle: { color: colors.accent, width: 2 },
          itemStyle: { color: colors.accent },
          areaStyle: { color: colors.accent, opacity: 0.12 },
        },
      ],
    }),
    [points],
  );
  if (points.length === 0) return null;
  return <EChart option={option} height={40} />;
};
