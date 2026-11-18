import { BulbOutlined, CloseOutlined, LoadingOutlined } from '@ant-design/icons';
import { Alert, Button, Spin, Tooltip } from 'antd';
import React, { useEffect, useMemo, useState } from 'react';

import { type AlertView, alertingApi } from '../../../entity/alerting';
import {
  type DeployMarker,
  type ExplainResponse,
  type PatternChange,
  type QueryNode,
  queryApi,
} from '../../../entity/query';
import { useTheme } from '../../../shared/theme';
import { formatInTimezone, useDisplayTimezone } from '../../../shared/time';
import { Card } from '../../../shared/ui/v2';
import {
  ERROR_LEVELS,
  WARN_LEVELS,
  andNodes,
  explainHeadline,
  explainLift,
  formatCount,
  isStrongLift,
  levelsNode,
} from '../lib/logInsights';
import { SegmentedControl, ShareBar, TonePill } from './LogsViewTabs';

export interface ExplainRange {
  from: number;
  to: number;
}

type Baseline = 'prev' | 'day' | 'week';
type Scope = 'errors' | 'all';

interface Props {
  projectId: string;
  query: QueryNode | null;
  range: ExplainRange;
  /** Adds "field = value" (exclude: "field ≠ value") to the search. */
  onFilter: (field: string, value: string, exclude: boolean) => void;
  onClose: () => void;
}

const SHIFT_MS: Record<Exclude<Baseline, 'prev'>, number> = {
  day: 86_400_000,
  week: 7 * 86_400_000,
};
const MAX_FIELDS = 8;
const MAX_PATTERNS = 6;
const REPLICA_SET_FIELD = 'k8s_replicaset';

const iso = (ms: number) => new Date(ms).toISOString();

/** The selection bar under the Logs histogram: explain, zoom to or clear the dragged range. */
export const LogExplainSelectionBar = ({
  range,
  isExplaining,
  onExplain,
  onZoom,
  onClear,
}: {
  range: ExplainRange;
  isExplaining: boolean;
  onExplain: () => void;
  onZoom: () => void;
  onClear: () => void;
}): React.JSX.Element => {
  const [timezone] = useDisplayTimezone();
  return (
    <div className="flex flex-wrap items-center gap-2">
      <span className="bg-accent-600 rounded-md px-2 py-0.5 text-xs whitespace-nowrap text-white">
        {formatInTimezone(range.from, 'HH:mm:ss', timezone)} –{' '}
        {formatInTimezone(range.to, 'HH:mm:ss', timezone)}
      </span>
      <Button type="primary" size="small" icon={<BulbOutlined />} onClick={onExplain}>
        {isExplaining ? 'Explaining this range' : 'Explain this spike'}
      </Button>
      <Button size="small" onClick={onZoom}>
        Zoom to range
      </Button>
      <Button size="small" onClick={onClear}>
        Clear selection
      </Button>
    </div>
  );
};

/** "What's different in this range": fields and patterns over-represented against a baseline, plus rollouts and alerts in it. */
export const LogExplainPanelComponent = ({
  projectId,
  query,
  range,
  onFilter,
  onClose,
}: Props): React.JSX.Element => {
  const { status, definition } = useTheme();
  const [timezone] = useDisplayTimezone();
  const [baseline, setBaseline] = useState<Baseline>('prev');
  const [scope, setScope] = useState<Scope>('errors');
  const [explained, setExplained] = useState<ExplainResponse | null>(null);
  const [patterns, setPatterns] = useState<PatternChange[] | null>(null);
  const [deploys, setDeploys] = useState<DeployMarker[]>([]);
  const [alerts, setAlerts] = useState<AlertView[]>([]);
  const [error, setError] = useState<string | null>(null);
  const [isLoading, setIsLoading] = useState(true);

  const length = range.to - range.from;
  const base = useMemo(
    () =>
      baseline === 'prev'
        ? { from: range.from - length, to: range.from - 1 }
        : { from: range.from - SHIFT_MS[baseline], to: range.to - SHIFT_MS[baseline] },
    [baseline, range, length],
  );
  const scoped = useMemo(
    () =>
      scope === 'errors' ? andNodes(query, levelsNode([...ERROR_LEVELS, ...WARN_LEVELS])) : query,
    [query, scope],
  );

  useEffect(() => {
    const abort = new AbortController();
    setIsLoading(true);
    setError(null);
    const current = { from: iso(range.from), to: iso(range.to) };
    const before = { from: iso(base.from), to: iso(base.to) };
    Promise.all([
      queryApi.explainRange(projectId, { query: scoped, current, baseline: before }, abort.signal),
      queryApi.getPatternChanges(
        projectId,
        { query: scoped, current, baseline: before, limit: MAX_PATTERNS },
        abort.signal,
      ),
    ])
      .then(([fields, changes]) => {
        setExplained(fields);
        setPatterns(changes.changes);
      })
      .catch((e: unknown) => {
        if (!abort.signal.aborted) setError(e instanceof Error ? e.message : String(e));
      })
      .finally(() => {
        if (!abort.signal.aborted) setIsLoading(false);
      });
    return () => abort.abort();
  }, [projectId, scoped, range, base]);

  // Rollouts and alerts don't depend on the baseline or scope.
  useEffect(() => {
    const abort = new AbortController();
    queryApi
      .getDeploys(
        projectId,
        { query, timeRange: { from: iso(range.from), to: iso(range.to) } },
        abort.signal,
      )
      .then((response) => setDeploys(response.markers))
      .catch(() => setDeploys([]));
    alertingApi
      .listAlerts(projectId)
      .then((list) =>
        setAlerts(
          list.filter((a) => {
            const at = new Date(a.firedAt ?? a.activeAt).valueOf();
            return at >= range.from && at <= range.to;
          }),
        ),
      )
      .catch(() => setAlerts([]));
    return () => abort.abort();
  }, [projectId, query, range]);

  const fields = explained?.fields.slice(0, MAX_FIELDS) ?? [];
  const summary = explainHeadline(fields, patterns ?? []);
  const baseLabel = {
    prev: `the ${formatDuration(length)} before`,
    day: `the same ${formatDuration(length)} yesterday`,
    week: `the same ${formatDuration(length)} last week`,
  }[baseline];
  const events = [
    ...deploys.map((d) => ({
      at: new Date(d.time).valueOf(),
      text: `Rollout: ${d.workload} (${d.namespace}, ${d.pods} pod${d.pods === 1 ? '' : 's'})`,
    })),
    ...alerts.map((a) => ({
      at: new Date(a.firedAt ?? a.activeAt).valueOf(),
      text: `Alert ${a.state === 'pending' ? 'pending' : 'fired'}: ${a.ruleName}`,
    })),
  ].sort((a, b) => a.at - b.at);

  return (
    <Card
      className="!border-accent-500"
      title={
        <span className="text-lg">
          What&apos;s different in {formatInTimezone(range.from, 'HH:mm', timezone)} –{' '}
          {formatInTimezone(range.to, 'HH:mm', timezone)}
        </span>
      }
      subtitle={
        explained
          ? `${formatCount(explained.currentTotal)} ${scope === 'errors' ? 'errors and warnings' : 'lines'} in the range, against ${baseLabel}`
          : `Against ${baseLabel}`
      }
      extra={
        <>
          <span className="text-xs text-gray-500 dark:text-gray-400">Compared with</span>
          <SegmentedControl
            label="Baseline"
            value={baseline}
            onChange={setBaseline}
            options={[
              { value: 'prev', label: 'Just before' },
              { value: 'day', label: 'Yesterday' },
              { value: 'week', label: 'Last week' },
            ]}
          />
          <SegmentedControl
            label="Which lines"
            value={scope}
            onChange={setScope}
            options={[
              { value: 'errors', label: 'Errors and warnings' },
              { value: 'all', label: 'All lines' },
            ]}
          />
          <Button
            size="small"
            type="text"
            aria-label="Close explanation"
            icon={<CloseOutlined />}
            onClick={onClose}
          />
        </>
      }
    >
      {error && <Alert type="error" showIcon message={error} className="mb-3" />}
      {isLoading && !explained ? (
        <div className="flex h-32 items-center justify-center">
          <Spin indicator={<LoadingOutlined spin />} />
        </div>
      ) : (
        <div className="flex flex-col gap-4">
          <div className="bg-accent-500/10 flex items-start gap-2.5 rounded-xl px-3.5 py-3">
            <span className="text-accent-600 dark:text-accent-400 font-bold">↳</span>
            {summary ? (
              <span>
                <strong className="font-semibold">{summary.headline}</strong>{' '}
                <span className="text-gray-500 dark:text-gray-400">{summary.detail}</span>
              </span>
            ) : (
              <span className="text-gray-500 dark:text-gray-400">
                Nothing stands out: no value&apos;s share grew against {baseLabel}.
              </span>
            )}
          </div>
          <div className="grid gap-5 lg:grid-cols-2">
            <div className="flex flex-col gap-2.5">
              <h3 className="m-0 text-sm font-semibold">Fields that stand out</h3>
              <div className="grid grid-cols-[minmax(0,1fr)_110px_54px_auto] items-center gap-x-3 gap-y-2 text-[13px]">
                <span className="text-xs text-gray-500 dark:text-gray-400">Value</span>
                <span className="text-xs text-gray-500 dark:text-gray-400">
                  In range vs baseline
                </span>
                <span className="text-right text-xs text-gray-500 dark:text-gray-400">Lift</span>
                <span />
                {fields.map((f) => {
                  const filterField = f.field === REPLICA_SET_FIELD ? 'k8s_pod_name' : f.field;
                  return (
                    <React.Fragment key={`${f.field}=${f.value}`}>
                      <span className="truncate font-mono" title={`${f.field} = ${f.value}`}>
                        <span className="text-gray-500 dark:text-gray-400">{f.field} =</span>{' '}
                        {f.value}
                      </span>
                      <span className="flex flex-col gap-0.5">
                        <ShareBar share={f.currentShare} color={status.bad} />
                        <ShareBar share={f.baselineShare} color={definition.colors.muted} />
                        <span className="text-xs text-gray-500 dark:text-gray-400">
                          {Math.round(f.currentShare * 100)}% vs {Math.round(f.baselineShare * 100)}
                          %
                        </span>
                      </span>
                      <strong
                        className="text-right font-semibold"
                        style={{ color: isStrongLift(f) ? status.bad : definition.colors.muted }}
                      >
                        {explainLift(f)}
                      </strong>
                      <span className="flex gap-1">
                        <Tooltip title="Show only lines with this value">
                          <Button
                            size="small"
                            onClick={() => onFilter(filterField, f.value, false)}
                          >
                            +
                          </Button>
                        </Tooltip>
                        <Tooltip title="Hide lines with this value">
                          <Button size="small" onClick={() => onFilter(filterField, f.value, true)}>
                            −
                          </Button>
                        </Tooltip>
                      </span>
                    </React.Fragment>
                  );
                })}
              </div>
              {fields.length === 0 && (
                <span className="text-sm text-gray-500 dark:text-gray-400">
                  No field stands out
                </span>
              )}
            </div>
            <div className="flex flex-col gap-2.5">
              <h3 className="m-0 text-sm font-semibold">Patterns that changed</h3>
              {(patterns ?? []).map((p) => (
                <span
                  key={p.hash}
                  className="flex items-center gap-2 rounded-xl bg-gray-50 px-2.5 py-2 dark:bg-gray-900/60"
                >
                  <TonePill
                    className="min-w-12 justify-center"
                    text={
                      p.change === 'new'
                        ? 'new'
                        : p.change === 'spike'
                          ? `×${p.ratio.toFixed(p.ratio < 100 ? 1 : 0)}`
                          : `−${Math.round((1 - p.ratio) * 100)}%`
                    }
                    tone={p.change === 'drop' ? 'info' : p.change === 'new' ? 'bad' : 'warn'}
                  />
                  <span
                    className="min-w-0 flex-1 truncate font-mono text-xs"
                    title={p.pattern || p.sample}
                  >
                    {p.pattern || p.sample || p.hash}
                  </span>
                  <span className="font-mono text-xs whitespace-nowrap text-gray-500 dark:text-gray-400">
                    {p.baseline.toLocaleString()} → {p.current.toLocaleString()}
                  </span>
                </span>
              ))}
              {patterns && patterns.length === 0 && (
                <span className="text-sm text-gray-500 dark:text-gray-400">No pattern changed</span>
              )}
              <h3 className="m-0 mt-1.5 text-sm font-semibold">Also happened</h3>
              {events.map((e, i) => (
                <span key={i} className="flex gap-2.5 text-[13px]">
                  <span className="font-mono text-gray-500 dark:text-gray-400">
                    {formatInTimezone(e.at, 'HH:mm', timezone)}
                  </span>
                  <span>{e.text}</span>
                </span>
              ))}
              {events.length === 0 && (
                <span className="text-sm text-gray-500 dark:text-gray-400">
                  No Kubernetes rollouts or alerts in this range
                </span>
              )}
            </div>
          </div>
          <span className="text-xs text-gray-500 dark:text-gray-400">
            How it works: Obzly counts each service, namespace, container, pod, ReplicaSet and level
            in the range and in the baseline (same length) from the per-minute counts, and lists the
            values whose share grew most. Values seen fewer than 20 times in the range are left out.
            {explained?.partial &&
              ' Too many combinations to count them all: the smallest are left out.'}
          </span>
        </div>
      )}
    </Card>
  );
};

const formatDuration = (ms: number): string => {
  const minutes = Math.max(1, Math.round(ms / 60_000));
  if (minutes < 120) return `${minutes} minutes`;
  const hours = Math.round(minutes / 60);
  if (hours < 48) return `${hours} hours`;
  return `${Math.round(hours / 24)} days`;
};
