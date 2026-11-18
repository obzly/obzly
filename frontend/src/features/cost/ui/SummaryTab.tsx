// Cost → Summary: the cloud at a glance, cost and posture side by side, every figure linking to
// the view behind it. Reads every Site in scope; a Site that fails is named, never counted as 0.
import {
  BulbOutlined,
  DollarOutlined,
  LineChartOutlined,
  SafetyOutlined,
  ToolOutlined,
} from '@ant-design/icons';
import { Button, Empty, Segmented, Tooltip } from 'antd';
import dayjs from 'dayjs';
import { type ReactNode, useEffect, useMemo, useState } from 'react';

import {
  type CommitmentCoverage,
  type CostAnomaly,
  type CostBudget,
  type ExploreResponse,
  costApi,
} from '../../../entity/cost';
import {
  type PostureAction,
  type PostureSummary,
  type TrendsResponse,
  postureApi,
} from '../../../entity/posture';
import { useTheme } from '../../../shared/theme';
import { Card, ChangePill, Donut, KpiTile } from '../../../shared/ui/v2';
import { mergeActions } from '../../posture/lib/actions';
import { scoreColor } from '../../posture/lib/format';
import {
  SEVERITY_COLOR,
  SEVERITY_LABEL,
  mergeSummary as mergePostureSummary,
  mergeTrends,
  sourcesLabel,
} from '../../posture/lib/merge';
import type { PostureView } from '../../posture/ui/PostureComponent';
import {
  KIND_LABEL,
  type SiteRecommendation,
  mergeCommitments,
  mergeRecommendations,
} from '../lib/advice';
import { formatChange, formatMoney, formatMoneyCompact } from '../lib/format';
import {
  type CostSite,
  type MergedSummary,
  type SiteOutcome,
  mergeExplore,
  mergeSummary,
} from '../lib/merge';
import { addDays, resolveRange } from '../lib/ranges';
import { fanOut } from '../lib/sites';
import {
  type CostBasis,
  anomalyRise,
  basisForecast,
  basisTotals,
  commitmentCoverage,
  dailyRange,
  monthProgress,
  monthStart,
  nextActions,
  openSavings,
  realizedSavings,
  sameDaysLastMonth,
  serviceRows,
  severityChanges,
  standardRows,
  topAnomaly,
  totalBudget,
} from '../lib/summary';
import { IconLabel, ProviderMark, ServiceIcon, StandardMark } from './CloudIcon';
import { Loading, SiteErrors, SourceBadge, TileRow } from './parts';

export type CostView = 'explorer' | 'recommendations' | 'budgets';

interface Props {
  sites: CostSite[];
  refresh: number;
  onView: (view: CostView) => void;
  /** Opens the Explorer on this month's services. */
  onExploreServices: () => void;
  onOpenPosture?: (view: PostureView) => void;
  onManageCloud?: () => void;
}

interface Base {
  summary: MergedSummary;
  outcomes: SiteOutcome<unknown>[];
  anomalies: CostAnomaly[];
  recommendations: SiteRecommendation[];
  coverage: CommitmentCoverage[];
  budgets: CostBudget[];
  posture: PostureSummary;
  postureOutcomes: SiteOutcome<unknown>[];
  trends?: TrendsResponse;
  actions: PostureAction[];
}

interface ByBasis {
  basis: CostBasis;
  daily: ExploreResponse;
  services: ExploreResponse;
  servicesBefore: ExploreResponse;
}

const BASIS_STORAGE_KEY = 'obzly-cost-summary-basis';

const readBasis = (): CostBasis => {
  try {
    return localStorage.getItem(BASIS_STORAGE_KEY) === 'billed' ? 'billed' : 'effective';
  } catch {
    return 'effective';
  }
};

const Meter = ({
  ratio,
  color,
  left,
  right,
}: {
  ratio: number;
  color: string;
  left: string;
  right?: string;
}) => (
  <span className="flex flex-col gap-1">
    <span className="block h-1.5 overflow-hidden rounded-full bg-[var(--v2-track)]">
      <span
        className="block h-full rounded-full"
        style={{ width: `${Math.min(100, Math.max(0, ratio * 100))}%`, background: color }}
      />
    </span>
    <span className="flex justify-between gap-2">
      <span>{left}</span>
      {right && <span>{right}</span>}
    </span>
  </span>
);

const LinkButton = ({ onClick, children }: { onClick: () => void; children: string }) => (
  <button
    type="button"
    onClick={onClick}
    className="text-accent-600 hover:text-accent-700 dark:text-accent-400 dark:hover:text-accent-300 cursor-pointer border-0 bg-transparent p-0 text-sm font-medium"
  >
    {children}
  </button>
);

export const SummaryTab = ({
  sites,
  refresh,
  onView,
  onExploreServices,
  onOpenPosture,
  onManageCloud,
}: Props) => {
  const { chartColors, status } = useTheme();
  const [basis, setBasis] = useState<CostBasis>(readBasis);
  const [base, setBase] = useState<Base>();
  const [byBasis, setByBasis] = useState<ByBasis>();

  useEffect(() => {
    const controller = new AbortController();
    const signal = controller.signal;
    const load = async () => {
      const [summaries, postureOutcomes] = await Promise.all([
        fanOut(sites, (siteId) => costApi.summary(siteId, signal)),
        fanOut(sites, (siteId) => postureApi.summary(siteId, signal)),
      ]);
      const summary = mergeSummary(summaries);
      const today = summary.today;
      const projects = new Map<string, { siteId: string; projectId: string }>();
      for (const source of summary.sources) {
        projects.set(`${source.siteId}|${source.projectId}`, {
          siteId: source.siteId,
          projectId: source.projectId,
        });
      }
      const [anomalies, recommendations, commitments, budgets, trends, actions] = await Promise.all(
        [
          fanOut(sites, (siteId) => costApi.anomalies(siteId, monthStart(today), today, signal)),
          fanOut(sites, (siteId) => costApi.recommendations(siteId, signal)),
          fanOut(sites, (siteId) => costApi.commitments(siteId, signal)),
          Promise.all(
            [...projects.values()].map(({ siteId, projectId }) =>
              costApi.budgets(siteId, projectId, signal).catch(() => [] as CostBudget[]),
            ),
          ),
          fanOut(sites, (siteId) =>
            postureApi.trends(
              siteId,
              { from: addDays(today, -7), to: today, granularity: 'daily', filters: {} },
              signal,
            ),
          ),
          fanOut(sites, (siteId) => postureApi.actions(siteId, signal)),
        ],
      );
      if (signal.aborted) return;
      const trendOutcomes = trends.filter((outcome) => outcome.value);
      setBase({
        summary,
        outcomes: summaries,
        anomalies: anomalies.flatMap((outcome) => outcome.value ?? []),
        recommendations: mergeRecommendations(recommendations).items,
        coverage: mergeCommitments(commitments).coverage,
        budgets: budgets.flat(),
        posture: mergePostureSummary(postureOutcomes),
        postureOutcomes,
        trends: trendOutcomes.length ? mergeTrends(trendOutcomes) : undefined,
        actions: mergeActions(actions).actions,
      });
    };
    void load();
    return () => controller.abort();
  }, [sites, refresh]);

  const today = base?.summary.today;
  useEffect(() => {
    if (!today) return;
    const controller = new AbortController();
    const signal = controller.signal;
    const explore = (
      range: { from: string; to: string },
      groupBy: 'provider' | 'service',
      daily: boolean,
    ) =>
      fanOut(sites, (siteId) =>
        costApi.explore(
          siteId,
          {
            ...range,
            granularity: daily ? 'daily' : 'monthly',
            groupBy,
            metric: basis,
            filters: {},
          },
          signal,
        ),
      );
    void Promise.all([
      explore(dailyRange(today), 'provider', true),
      explore(resolveRange('mtd', today), 'service', false),
      explore(sameDaysLastMonth(today), 'service', false),
    ]).then(([daily, services, servicesBefore]) => {
      if (signal.aborted) return;
      setByBasis({
        basis,
        daily: mergeExplore(daily),
        services: mergeExplore(services),
        servicesBefore: mergeExplore(servicesBefore),
      });
    });
    return () => controller.abort();
  }, [sites, today, basis, refresh]);

  const currency = base?.summary.currencies[0];
  const estimatedDays = useMemo(
    () => new Set((currency?.daily ?? []).filter((day) => day.estimated).map((day) => day.day)),
    [currency],
  );
  const totals = useMemo(
    () =>
      byBasis && currency && today
        ? basisTotals(byBasis.daily, currency.currency, today, estimatedDays)
        : undefined,
    [byBasis, currency, today, estimatedDays],
  );

  if (!base) return <Loading />;
  const { summary, posture } = base;
  const hasCost = summary.sources.length > 0 || summary.currencies.length > 0;
  const hasPosture = posture.standards.length > 0;
  if (!hasCost && !hasPosture) {
    return (
      <>
        <SiteErrors outcomes={[...base.outcomes, ...base.postureOutcomes]} />
        <Empty
          className="py-12"
          description="No cloud data yet. Turn on Cost or Security posture for a cloud connection; the probe delivers the first data within minutes."
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

  const code = currency?.currency ?? 'USD';
  const money = (amount: number) => formatMoney(amount, code);
  const progress = monthProgress(summary.today);
  const basisLabel = basis === 'billed' ? 'Billed cost' : 'Effective cost (amortized)';
  const ready = byBasis?.basis === basis ? totals : undefined;
  const forecast =
    currency && ready
      ? basisForecast(currency.forecast, currency.monthToDate, ready.monthToDate)
      : undefined;
  const budget = totalBudget(base.budgets, code);
  const savings = openSavings(base.recommendations, code);
  const realized = realizedSavings(base.recommendations, code);
  const anomaly = topAnomaly(base.anomalies, code, summary.today);
  const coverage = commitmentCoverage(base.coverage);
  const services =
    byBasis?.basis === basis
      ? serviceRows(byBasis.services, byBasis.servicesBefore, code)
      : undefined;
  const standards = standardRows(posture, summary.today);
  const severities = severityChanges(posture, base.trends);
  const openTotal = severities.reduce((sum, row) => sum + row.count, 0);
  const actions = nextActions(base.recommendations, base.actions, currency?.currency);
  const connections = [
    ...new Map(
      summary.sources.map((source) => [`${source.siteId}|${source.connectionId}`, source]),
    ).values(),
  ];
  const latestDelivery = summary.sources
    .map((source) => source.deliveredAt)
    .sort()
    .pop();
  const estimated = (currency?.daily ?? []).filter(
    (day) => day.estimated && day.day >= monthStart(summary.today),
  );
  const change = ready ? formatChange(ready.monthToDate, ready.sameDaysLastMonth) : '';
  const providerColor = (provider: string, index: number) =>
    provider === 'aws'
      ? chartColors[0]
      : provider === 'azure'
        ? chartColors[1 % chartColors.length]
        : chartColors[(index + 2) % chartColors.length];
  const highest = ready?.days.reduce<(typeof ready.days)[number] | undefined>(
    (best, day) => (!best || day.total > best.total ? day : best),
    undefined,
  );
  const anomalyDays = new Set(
    base.anomalies
      .filter((item) => item.currency === code && item.status === 'open')
      .map((item) => item.day),
  );

  return (
    <div className="flex flex-col gap-4">
      <SiteErrors outcomes={[...base.outcomes, ...base.postureOutcomes]} />

      <div className="flex flex-wrap items-center gap-3">
        <span className="min-w-[240px] flex-1 text-sm text-gray-500 dark:text-gray-400">
          {connections.length} connection{connections.length === 1 ? '' : 's'}
          {connections.length > 0 && (
            <>
              {' · '}
              {connections
                .slice(0, 3)
                .map(
                  (source) =>
                    `${source.provider === 'aws' ? 'AWS' : 'Azure'} ${source.connectionName}`,
                )
                .join(', ')}
              {connections.length > 3 && ` and ${connections.length - 3} more`}
            </>
          )}
          {' · '}
          {dayjs(summary.today).format('MMMM')}, day {progress.day} of {progress.days}
        </span>
        {summary.sources.some((source) => source.source === 'export') && (
          <SourceBadge source="export" />
        )}
        {summary.sources.some((source) => source.source === 'api') && <SourceBadge source="api" />}
        {latestDelivery && (
          <span className="text-xs text-gray-500 dark:text-gray-400">
            updated {dayjs(latestDelivery).fromNow()}
          </span>
        )}
        {estimated.length > 0 && (
          <Tooltip title="These days are still estimated and may change once the provider closes them">
            <span className="rounded-full bg-amber-500/15 px-2 py-0.5 text-xs font-medium text-[var(--v2-orange)]">
              {estimated.length} day{estimated.length === 1 ? '' : 's'} estimated
            </span>
          </Tooltip>
        )}
        <Segmented
          aria-label="Cost basis"
          value={basis}
          onChange={(value) => {
            const next = value as CostBasis;
            setBasis(next);
            try {
              localStorage.setItem(BASIS_STORAGE_KEY, next);
            } catch {
              /* back to effective next time */
            }
          }}
          options={[
            { value: 'effective', label: 'Effective' },
            { value: 'billed', label: 'Billed' },
          ]}
        />
      </div>

      {summary.currencies.length > 1 && (
        <span className="text-xs text-gray-500 dark:text-gray-400">
          Showing {code}; spend in{' '}
          {summary.currencies
            .slice(1)
            .map((item) => item.currency)
            .join(', ')}{' '}
          is on the Overview tab (currencies are never added together).
        </span>
      )}

      <TileRow label="Spend">
        <KpiTile
          label="Month to date"
          icon={<DollarOutlined />}
          value={ready ? money(ready.monthToDate) : byBasis?.basis === basis ? 'n/a' : '…'}
          change={
            change
              ? {
                  text: change,
                  tone: ready!.monthToDate > ready!.sameDaysLastMonth ? 'bad' : 'good',
                }
              : undefined
          }
          note={change ? `${basisLabel} · vs the same days of last month` : basisLabel}
          trend={
            ready && ready.days.length > 1
              ? ready.days.slice(-14).map((day) => day.total)
              : undefined
          }
          color={chartColors[0]}
          onClick={onExploreServices}
        />
        <KpiTile
          label={`Forecast, ${dayjs(summary.today).format('MMMM')}`}
          icon={<LineChartOutlined />}
          value={
            forecast === undefined ? (byBasis?.basis === basis ? 'n/a' : '…') : money(forecast)
          }
          change={
            budget
              ? { text: `budget ${formatMoneyCompact(budget.amount, code)}`, tone: 'neutral' }
              : undefined
          }
          note={
            forecast !== undefined && budget ? (
              <Meter
                ratio={forecast / budget.amount}
                color={forecast > budget.amount ? status.bad : chartColors[1 % chartColors.length]}
                left={`${Math.round((forecast / budget.amount) * 100)}% of ${budget.count > 1 ? `${budget.count} budgets` : 'budget'}`}
                right={budget.thresholdPercent ? `alert at ${budget.thresholdPercent}%` : undefined}
              />
            ) : forecast === undefined ? (
              'No provider forecast yet'
            ) : (
              'No whole-account budget set'
            )
          }
          color={chartColors[1 % chartColors.length]}
          onClick={() => onView('budgets')}
        />
        <KpiTile
          label="Open savings"
          icon={<BulbOutlined />}
          value={`${money(savings.total)}/mo`}
          change={{
            text: `${savings.count} action${savings.count === 1 ? '' : 's'}`,
            tone: 'neutral',
          }}
          note={
            [
              savings.topKind && `${KIND_LABEL[savings.topKind].toLowerCase()} leads`,
              savings.unknown > 0 && `${savings.unknown} not priced yet`,
            ]
              .filter(Boolean)
              .join(' · ') || 'Open and planned recommendations'
          }
          color={chartColors[2 % chartColors.length]}
          onClick={() => onView('recommendations')}
        />
        <KpiTile
          label="Realized savings"
          icon={<ToolOutlined />}
          value={realized.measured ? `${money(realized.realized)}/mo` : 'n/a'}
          change={{
            text: `${realized.measured} measured`,
            tone: 'neutral',
          }}
          note={
            realized.measured && realized.expected > 0 ? (
              <Meter
                ratio={realized.realized / realized.expected}
                color={status.ok}
                left={`${Math.round((realized.realized / realized.expected) * 100)}% of what was planned`}
                right={realized.pending ? `${realized.pending} waiting` : undefined}
              />
            ) : realized.pending ? (
              `${realized.pending} done, measured after 14 days`
            ) : (
              'Nothing marked done yet'
            )
          }
          color={status.ok}
          onClick={() => onView('recommendations')}
        />
      </TileRow>

      <div className="grid gap-4 lg:grid-cols-[minmax(0,1.6fr)_minmax(0,1fr)]">
        <Card
          title="Daily spend"
          subtitle={`${basisLabel}, ${code}, stacked by cloud; faded days are still estimated`}
          extra={
            <span className="flex flex-wrap items-center gap-3 text-xs text-gray-500 dark:text-gray-400">
              {(ready?.providers ?? []).map((provider, index) => (
                <span key={provider} className="flex items-center gap-1.5">
                  <span
                    className="h-2.5 w-2.5 rounded-sm"
                    style={{ background: providerColor(provider, index) }}
                  />
                  {provider === 'aws' ? 'AWS' : provider === 'azure' ? 'Azure' : provider}
                </span>
              ))}
              <span className="flex items-center gap-1.5">
                <span className="h-2.5 w-2.5 rounded-full" style={{ background: status.bad }} />
                Anomaly
              </span>
            </span>
          }
        >
          {!ready ? (
            byBasis ? (
              <div className="py-10 text-center text-sm text-gray-500 dark:text-gray-400">
                No {code} spend this month
              </div>
            ) : (
              <Loading className="py-10" />
            )
          ) : (
            <div className="flex flex-col gap-3">
              <DailyBars
                days={ready.days}
                providers={ready.providers}
                colorOf={providerColor}
                anomalyDays={anomalyDays}
                anomalyColor={status.bad}
                money={money}
              />
              <div className="grid grid-cols-[repeat(auto-fit,minmax(150px,1fr))] gap-3 border-t border-gray-200 pt-3 dark:border-gray-700">
                <Stat
                  label="Highest day"
                  value={
                    highest
                      ? `${dayjs(highest.day).format('MMM D')} · ${money(highest.total)}`
                      : 'n/a'
                  }
                />
                <Stat
                  label="Anomaly"
                  value={
                    anomaly ? (
                      <button
                        type="button"
                        onClick={() => onView('budgets')}
                        className="hover:text-accent-600 cursor-pointer border-0 bg-transparent p-0 text-left font-semibold text-inherit"
                      >
                        {anomaly.service || 'Whole account'}{' '}
                        {anomaly.subAccountName || anomaly.subAccount} {anomalyRise(anomaly)}
                      </button>
                    ) : (
                      'None open this month'
                    )
                  }
                />
                <Stat
                  label="Commitment coverage"
                  value={coverage === undefined ? 'n/a' : `${Math.round(coverage)}% of eligible`}
                />
              </div>
            </div>
          )}
        </Card>

        <Card
          title="Top services"
          subtitle={`Month to date, change vs the same days of last month`}
          extra={<LinkButton onClick={onExploreServices}>Explorer</LinkButton>}
        >
          {!services ? (
            <Loading className="py-10" />
          ) : services.length === 0 ? (
            <div className="py-10 text-center text-sm text-gray-500 dark:text-gray-400">
              No spend this month
            </div>
          ) : (
            <div className="flex flex-col gap-3">
              {services.map((row, index) => {
                const max = services[0].total || 1;
                return (
                  <button
                    key={row.key || '__none'}
                    type="button"
                    onClick={onExploreServices}
                    className="flex cursor-pointer flex-col gap-1.5 border-0 bg-transparent p-0 text-left"
                  >
                    <span className="flex items-center justify-between gap-3 text-sm">
                      <span className="min-w-0 text-gray-800 dark:text-gray-100">
                        <IconLabel icon={<ServiceIcon text={row.key} size={18} />}>
                          {row.label}
                        </IconLabel>
                      </span>
                      <span className="flex items-center gap-2 whitespace-nowrap">
                        <strong className="font-semibold text-gray-900 dark:text-gray-50">
                          {money(row.total)}
                        </strong>
                        {row.change !== undefined && (
                          <ChangePill
                            text={`${row.change >= 0 ? '+' : '−'}${Math.abs(row.change).toFixed(0)}%`}
                            // More spend is bad news; flat is neither.
                            tone={
                              Math.abs(row.change) < 1 ? 'neutral' : row.change > 0 ? 'bad' : 'good'
                            }
                          />
                        )}
                      </span>
                    </span>
                    <span className="block h-1.5 overflow-hidden rounded-full bg-[var(--v2-track)]">
                      <span
                        className="block h-full rounded-full"
                        style={{
                          width: `${Math.max(2, (row.total / max) * 100)}%`,
                          background: chartColors[index % chartColors.length],
                        }}
                      />
                    </span>
                  </button>
                );
              })}
            </div>
          )}
        </Card>
      </div>

      <Card
        title="Posture by standard"
        subtitle="Share of controls passing; the badge says where the findings come from"
        extra={
          onOpenPosture && (
            <LinkButton onClick={() => onOpenPosture('coverage')}>Coverage</LinkButton>
          )
        }
      >
        {standards.length === 0 ? (
          <div className="py-6 text-center text-sm text-gray-500 dark:text-gray-400">
            No standard evaluated yet
          </div>
        ) : (
          <div className="grid grid-cols-[repeat(auto-fit,minmax(240px,1fr))] gap-3">
            {standards.map((standard) => {
              const source = sourcesLabel(standard.sources);
              const color = scoreColor(standard.percent, status);
              return (
                <button
                  key={standard.standard}
                  type="button"
                  onClick={() => onOpenPosture?.('overview')}
                  disabled={!onOpenPosture}
                  className="flex cursor-pointer items-center gap-3 rounded-xl border border-gray-200 bg-gray-50 p-3 text-left disabled:cursor-default dark:border-gray-700 dark:bg-gray-900/40"
                >
                  <Donut
                    size={88}
                    label={`${standard.name} ${standard.percent}% passing`}
                    parts={[
                      { key: 'passed', value: standard.percent, color },
                      {
                        key: 'rest',
                        value: Math.max(0, 100 - standard.percent),
                        color: 'transparent',
                      },
                    ]}
                    center={`${Math.round(standard.percent)}%`}
                  />
                  <span className="flex min-w-0 flex-col gap-1">
                    <span className="text-sm font-semibold text-gray-900 dark:text-gray-50">
                      <IconLabel
                        icon={<StandardMark standard={standard.standard} name={standard.name} />}
                      >
                        {standard.name}
                      </IconLabel>
                    </span>
                    <span className="text-xs text-gray-500 dark:text-gray-400">
                      {standard.passed} of {standard.evaluated} controls passing
                    </span>
                    <span className="flex flex-wrap gap-1.5">
                      <span
                        className={`rounded-full px-2 py-0.5 text-[11px] font-medium ${
                          source === 'Native'
                            ? 'bg-[var(--v2-green-soft,rgba(16,185,129,0.14))] text-[var(--v2-green,#10b981)]'
                            : 'bg-amber-500/15 text-[var(--v2-orange)]'
                        }`}
                      >
                        {source}
                      </span>
                      {standard.weekChange !== undefined && (
                        <ChangePill
                          text={`${standard.weekChange >= 0 ? '+' : '−'}${Math.abs(standard.weekChange)} pt this week`}
                          tone={
                            standard.weekChange === 0
                              ? 'neutral'
                              : standard.weekChange > 0
                                ? 'good'
                                : 'bad'
                          }
                        />
                      )}
                    </span>
                  </span>
                </button>
              );
            })}
          </div>
        )}
      </Card>

      <div className="grid gap-4 lg:grid-cols-[minmax(0,1fr)_minmax(0,1.6fr)]">
        <Card
          title="Open findings"
          subtitle={
            base.trends
              ? 'By severity, all connections; change over the last 7 days'
              : 'By severity, all connections'
          }
          extra={
            onOpenPosture && (
              <LinkButton onClick={() => onOpenPosture('findings')}>Findings</LinkButton>
            )
          }
        >
          <div className="flex flex-wrap items-center gap-5">
            <Donut
              size={150}
              label={`${openTotal} open findings: ${severities.map((row) => `${row.count} ${row.severity}`).join(', ')}`}
              parts={severities
                .filter((row) => row.count > 0)
                .map((row) => ({
                  key: row.severity,
                  value: row.count,
                  color: SEVERITY_COLOR[row.severity],
                }))}
              center={openTotal.toLocaleString()}
              centerSub="open"
            />
            <span className="flex min-w-[160px] flex-1 flex-col gap-2.5">
              {severities.map((row) => (
                <span key={row.severity} className="flex items-center gap-2 text-sm">
                  <span
                    className="h-2.5 w-2.5 rounded-full"
                    style={{ background: SEVERITY_COLOR[row.severity] }}
                  />
                  <span className="flex-1 text-gray-700 dark:text-gray-200">
                    {SEVERITY_LABEL[row.severity]}
                  </span>
                  <strong className="font-semibold text-gray-900 dark:text-gray-50">
                    {row.count}
                  </strong>
                  {row.change !== undefined && row.change !== 0 && (
                    <ChangePill
                      text={`${row.change > 0 ? '+' : '−'}${Math.abs(row.change)}`}
                      // More open findings is bad news.
                      tone={row.change > 0 ? 'bad' : 'good'}
                    />
                  )}
                </span>
              ))}
            </span>
          </div>
        </Card>

        <Card
          title="Next best actions"
          subtitle="Cost recommendations and posture fixes, the highest impact of each first"
          extra={<LinkButton onClick={() => onView('recommendations')}>All actions</LinkButton>}
        >
          {actions.length === 0 ? (
            <div className="py-6 text-center text-sm text-gray-500 dark:text-gray-400">
              Nothing open right now
            </div>
          ) : (
            <div className="flex flex-col gap-1">
              {actions.map((action) => (
                <ActionRow
                  key={action.key}
                  action={action}
                  money={(amount, currencyCode) => `${formatMoney(amount, currencyCode)}/mo`}
                  onOpen={() =>
                    action.kind === 'cost' ? onView('recommendations') : onOpenPosture?.('actions')
                  }
                  colors={{ cost: status.ok, posture: status.bad, warn: status.warn }}
                />
              ))}
            </div>
          )}
        </Card>
      </div>
    </div>
  );
};

const Stat = ({ label, value }: { label: string; value: ReactNode }) => (
  <span className="flex flex-col">
    <span className="text-xs text-gray-500 dark:text-gray-400">{label}</span>
    <strong className="text-sm font-semibold text-gray-900 dark:text-gray-50">{value}</strong>
  </span>
);

const BAR_HEIGHT = 180;

const DailyBars = ({
  days,
  providers,
  colorOf,
  anomalyDays,
  anomalyColor,
  money,
}: {
  days: { day: string; amounts: Record<string, number>; total: number; estimated: boolean }[];
  providers: string[];
  colorOf: (provider: string, index: number) => string;
  anomalyDays: ReadonlySet<string>;
  anomalyColor: string;
  money: (amount: number) => string;
}) => {
  const max = Math.max(1, ...days.map((day) => day.total));
  const ticks = [
    0,
    Math.floor((days.length - 1) / 3),
    Math.floor(((days.length - 1) * 2) / 3),
    days.length - 1,
  ];
  return (
    <>
      <div
        role="img"
        aria-label={`Daily spend for ${days.length} days this month`}
        className="flex items-end gap-1"
        style={{ height: BAR_HEIGHT }}
      >
        {days.map((day) => (
          <Tooltip
            key={day.day}
            title={
              <span>
                {dayjs(day.day).format('MMM D')}: {money(day.total)}
                {day.estimated && ' (estimated)'}
                {anomalyDays.has(day.day) && ' · anomaly'}
                {providers.map((provider) =>
                  day.amounts[provider] ? (
                    <span key={provider} className="block">
                      {provider === 'aws' ? 'AWS' : provider === 'azure' ? 'Azure' : provider}:{' '}
                      {money(day.amounts[provider])}
                    </span>
                  ) : null,
                )}
              </span>
            }
          >
            <span
              className="relative flex min-w-0 flex-1 cursor-default flex-col justify-end"
              style={{ height: '100%' }}
            >
              {anomalyDays.has(day.day) && (
                <span
                  className="mx-auto mb-1 h-2 w-2 shrink-0 rounded-full"
                  style={{ background: anomalyColor }}
                />
              )}
              <span
                className="flex flex-col-reverse overflow-hidden rounded-[3px]"
                style={{
                  height: `${Math.max(day.total > 0 ? 2 : 1, (Math.max(0, day.total) / max) * (BAR_HEIGHT - 14))}px`,
                  opacity: day.estimated ? 0.45 : 1,
                  background: day.total > 0 ? undefined : 'var(--v2-track)',
                }}
              >
                {providers.map((provider, index) =>
                  day.amounts[provider] > 0 ? (
                    <span
                      key={provider}
                      style={{
                        flexGrow: day.amounts[provider],
                        background: colorOf(provider, index),
                      }}
                    />
                  ) : null,
                )}
              </span>
            </span>
          </Tooltip>
        ))}
      </div>
      <div className="flex justify-between text-xs text-gray-500 dark:text-gray-400">
        {[...new Set(ticks)].map((index) => (
          <span key={index}>{days[index] ? dayjs(days[index].day).format('MMM D') : ''}</span>
        ))}
      </div>
    </>
  );
};

const ActionRow = ({
  action,
  money,
  onOpen,
  colors,
}: {
  action: ReturnType<typeof nextActions>[number];
  money: (amount: number, currency: string) => string;
  onOpen: () => void;
  colors: { cost: string; posture: string; warn: string };
}) => {
  const cost = action.kind === 'cost';
  const severity = !cost ? action.item.severity : undefined;
  const color = cost
    ? colors.cost
    : severity === 'critical' || severity === 'high'
      ? colors.posture
      : colors.warn;
  const title = cost
    ? action.item.action || `${action.item.kind} ${action.item.resourceName}`
    : action.item.title;
  const sub = cost
    ? [
        action.item.resourceName || action.item.resourceKey,
        action.item.account,
        action.item.region,
        action.item.connectionName,
      ]
        .filter(Boolean)
        .join(' · ')
    : [
        action.item.findings.length
          ? `closes ${action.item.findings.length} finding${action.item.findings.length === 1 ? '' : 's'}`
          : action.item.detail,
        action.item.account,
        action.item.connectionName,
      ]
        .filter(Boolean)
        .join(' · ');
  const impact = cost
    ? action.item.monthlySavings === null
      ? 'not priced'
      : money(action.item.monthlySavings, action.item.currency)
    : severity
      ? SEVERITY_LABEL[severity]
      : 'Coverage';
  return (
    <button
      type="button"
      onClick={onOpen}
      className="flex cursor-pointer items-center gap-3 rounded-xl border-0 bg-transparent px-2 py-2 text-left hover:bg-gray-50 dark:hover:bg-gray-700/40"
    >
      <span
        className="flex h-8 w-8 shrink-0 items-center justify-center rounded-lg"
        style={{ color, background: `color-mix(in srgb, ${color} 15%, transparent)` }}
      >
        {cost ? <DollarOutlined /> : <SafetyOutlined />}
      </span>
      <span className="flex min-w-0 flex-1 flex-col">
        <span className="truncate text-sm text-gray-800 dark:text-gray-100">
          {cost && <ProviderMark provider={action.item.provider} size={14} />} {title}
        </span>
        <span className="truncate text-xs text-gray-500 dark:text-gray-400">{sub}</span>
      </span>
      <span className="flex shrink-0 flex-col items-end">
        <strong className="text-sm font-semibold" style={{ color }}>
          {impact}
        </strong>
        <span className="text-xs text-gray-500 dark:text-gray-400">
          {cost ? 'Cost' : 'Posture'}
        </span>
      </span>
    </button>
  );
};
