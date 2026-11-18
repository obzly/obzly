import { LoadingOutlined } from '@ant-design/icons';
import { Alert, Button, Spin } from 'antd';
import React, { useEffect, useState } from 'react';

import { type LogSampleRule, projectApi } from '../../../entity/projects';
import { type NoisyPattern, type VolumeResponse, queryApi } from '../../../entity/query';
import { useTheme } from '../../../shared/theme';
import { formatInTimezone } from '../../../shared/time';
import { Card, KpiTile } from '../../../shared/ui/v2';
import {
  currentSampleRate,
  formatBytes,
  formatCount,
  samplingPreview,
  samplingSuggestion,
} from '../lib/logInsights';
import { levelColor } from '../lib/logLevels';
import { SegmentedControl, ShareBar, StackedColumns, TonePill } from './LogsViewTabs';
import { SamplingRuleModalComponent, type SamplingTarget } from './SamplingRuleModalComponent';

interface Props {
  projectId: string;
}

const DAYS = 14;

/** Logs › Volume & noise: lines per day by service, by level, the noisiest patterns and sampling rules for them. */
export const LogVolumeTabComponent = ({ projectId }: Props): React.JSX.Element => {
  const { chartColors, definition } = useTheme();
  const [response, setResponse] = useState<VolumeResponse | null>(null);
  const [loadedAt, setLoadedAt] = useState(0);
  const [error, setError] = useState<string | null>(null);
  const [isLoading, setIsLoading] = useState(true);
  const [measure, setMeasure] = useState<'lines' | 'bytes'>('lines');
  const [rules, setRules] = useState<LogSampleRule[] | null>(null);
  const [ruleFor, setRuleFor] = useState<{ target: SamplingTarget; keep: number } | null>(null);

  const loadRules = () => {
    projectApi
      .getRetentionPolicies(projectId)
      .then((policies) => setRules(policies.find((p) => p.signal === 'logs')?.sampleRules ?? []))
      .catch(() => setRules(null));
  };

  useEffect(() => {
    const abort = new AbortController();
    setIsLoading(true);
    setError(null);
    queryApi
      .getVolume(projectId, DAYS, abort.signal)
      .then((result) => {
        setResponse(result);
        setLoadedAt(Date.now());
      })
      .catch((e: unknown) => {
        if (!abort.signal.aborted) setError(e instanceof Error ? e.message : String(e));
      })
      .finally(() => {
        if (!abort.signal.aborted) setIsLoading(false);
      });
    loadRules();
    return () => abort.abort();
  }, [projectId]);

  const bytesPerLine = response?.bytesPerLine ?? 0;
  const scale = measure === 'bytes' ? bytesPerLine : 1;
  const levelsTotal = response?.levels.reduce((sum, l) => sum + l.count, 0) ?? 0;
  // Seconds since 00:00 UTC (the day the backend counts "today" in) when loaded.
  const secondsToday = Math.max((loadedAt % 86_400_000) / 1000, 1);
  const todayChange =
    response && response.yesterdaySoFar > 0
      ? (response.today / response.yesterdaySoFar - 1) * 100
      : null;
  const suggestions = (response?.noisy ?? []).filter((n) => {
    const suggestion = samplingSuggestion(n);
    return (
      suggestion.keep !== null &&
      (rules === null || currentSampleRate(rules, n.service, n.level, n.hash) >= 1)
    );
  });
  const couldSave =
    bytesPerLine > 0
      ? suggestions.reduce(
          (sum, n) =>
            sum +
            (samplingPreview(n.perDay, samplingSuggestion(n).keep ?? 100, bytesPerLine)
              .savedBytesPerMonth ?? 0),
          0,
        )
      : null;
  const serviceColor = (i: number, other?: boolean) =>
    other ? definition.colors.muted : chartColors[i % chartColors.length];

  const ruleRate = (n: NoisyPattern): number | null =>
    rules === null ? null : currentSampleRate(rules, n.service, n.level, n.hash);
  const hasPatternRule = (n: NoisyPattern) =>
    rules?.some(
      (r) =>
        r.pattern === n.hash &&
        (!r.service || r.service === n.service) &&
        (!r.level || r.level.toLowerCase() === n.level.toLowerCase()),
    ) ?? false;

  return (
    <div className="flex flex-col gap-4">
      {error && <Alert type="error" showIcon message={error} />}
      {response?.partial && (
        <Alert
          type="info"
          showIcon
          message="More groups than one query counts: the smallest are left out."
        />
      )}
      <section aria-label="Volume" className="grid grid-cols-2 gap-3 lg:grid-cols-4">
        <KpiTile
          label="Lines today"
          value={response ? formatCount(response.today) : 'n/a'}
          change={
            todayChange === null
              ? undefined
              : {
                  text: `${todayChange >= 0 ? '+' : '−'}${Math.abs(Math.round(todayChange))}%`,
                  tone: 'neutral',
                }
          }
          note={
            response
              ? `${formatCount(response.today / secondsToday)} per second on average (UTC day); against yesterday at this time`
              : undefined
          }
        />
        <KpiTile
          label="Stored today"
          value={
            response && bytesPerLine > 0 ? `≈ ${formatBytes(response.today * bytesPerLine)}` : 'n/a'
          }
          icon={<TonePill text="compressed" tone="neutral" />}
          note="estimated from the project's average stored line size"
        />
        <KpiTile
          label="Stored in total"
          value={response ? formatBytes(response.storedBytes) : 'n/a'}
          icon={<TonePill text="all retention" tone="neutral" />}
          note="compressed segments, hot disk and object storage"
        />
        <KpiTile
          label="Could save"
          value={couldSave === null ? 'n/a' : formatBytes(couldSave)}
          unit={couldSave === null ? undefined : '/ month'}
          change={{
            text: `${suggestions.length} suggestion${suggestions.length === 1 ? '' : 's'}`,
            tone: suggestions.length > 0 ? 'good' : 'neutral',
          }}
          note="without touching error lines"
        />
      </section>

      <div className="grid gap-4 lg:grid-cols-3">
        <Card
          className="lg:col-span-2"
          title={
            measure === 'lines' ? 'Lines per day by service' : 'Stored bytes per day by service'
          }
          subtitle={`Last ${DAYS} days (UTC) · stacked${measure === 'bytes' ? ' · estimated from the average stored line size' : ''}`}
          extra={
            <SegmentedControl
              label="Measure"
              value={measure}
              onChange={setMeasure}
              options={[
                { value: 'lines', label: 'Lines' },
                { value: 'bytes', label: 'Bytes' },
              ]}
            />
          }
        >
          {isLoading && !response ? (
            <div className="flex h-[150px] items-center justify-center">
              <Spin indicator={<LoadingOutlined spin />} />
            </div>
          ) : response && measure === 'bytes' && bytesPerLine === 0 ? (
            <div className="py-10 text-center text-sm text-gray-500 dark:text-gray-400">
              No stored size yet (n/a)
            </div>
          ) : response ? (
            <>
              <StackedColumns
                gap={6}
                label={`Daily log volume by service for ${DAYS} days`}
                columns={response.days.map((at, d) => ({
                  key: at,
                  title: `${formatInTimezone(new Date(at).valueOf(), 'MMM D', 'UTC')}: ${
                    measure === 'bytes'
                      ? formatBytes(response.services.reduce((s, x) => s + x.counts[d], 0) * scale)
                      : `${response.services.reduce((s, x) => s + x.counts[d], 0).toLocaleString()} lines`
                  }`,
                  segments: response.services.map((s, i) => ({
                    value: s.counts[d] * scale,
                    color: serviceColor(i, s.other),
                  })),
                }))}
              />
              <div className="mt-1 flex justify-between text-xs text-gray-500 dark:text-gray-400">
                <span>
                  {formatInTimezone(new Date(response.days[0]).valueOf(), 'MMM D', 'UTC')}
                </span>
                <span>today</span>
              </div>
              <div className="mt-2 flex flex-wrap gap-x-3 gap-y-1 text-xs text-gray-500 dark:text-gray-400">
                {response.services.map((s, i) => (
                  <span key={s.name + i} className="inline-flex items-center gap-1.5">
                    <i
                      className="h-2.5 w-2.5 rounded-sm"
                      style={{ background: serviceColor(i, s.other) }}
                    />
                    {s.name || 'no service'}
                  </span>
                ))}
              </div>
            </>
          ) : null}
        </Card>
        <Card title="By level" subtitle="Share of lines, last 7 days">
          <div className="flex flex-col gap-3">
            {response?.levels.map((l) => (
              <span key={l.name} className="flex flex-col gap-1">
                <span className="flex justify-between text-[13px]">
                  <span>{l.name || 'unknown'}</span>
                  <span>
                    <strong className="font-semibold">{formatCount(l.count)}</strong>{' '}
                    <span className="text-xs text-gray-500 dark:text-gray-400">
                      {levelsTotal > 0 ? `${Math.round((l.count / levelsTotal) * 100)}%` : 'n/a'}
                    </span>
                  </span>
                </span>
                <ShareBar
                  share={levelsTotal > 0 ? l.count / levelsTotal : 0}
                  color={levelColor(l.name)}
                  className="h-2"
                />
              </span>
            ))}
            {response && response.levels.length === 0 && (
              <span className="text-sm text-gray-500 dark:text-gray-400">No logs in 7 days</span>
            )}
          </div>
        </Card>
      </div>

      <Card
        title="Noisiest patterns"
        subtitle="Biggest by lines over the last 7 days, with a suggestion; nothing changes until you add a rule"
      >
        <div className="overflow-x-auto">
          <table className="w-full text-sm">
            <thead>
              <tr className="text-left text-xs text-gray-500 dark:text-gray-400">
                <th className="py-2 pr-3 font-medium">Pattern</th>
                <th className="py-2 pr-3 font-medium">Service</th>
                <th className="py-2 pr-3 text-right font-medium">Lines / day</th>
                <th className="py-2 pr-3 text-right font-medium">Stored / day</th>
                <th className="py-2 pr-3 font-medium">Suggestion</th>
                <th className="py-2 pr-3 text-right font-medium">Saves / month</th>
                <th className="py-2 font-medium" />
              </tr>
            </thead>
            <tbody>
              {response?.noisy.map((n) => {
                const suggestion = samplingSuggestion(n);
                const saves =
                  suggestion.keep === null
                    ? null
                    : samplingPreview(n.perDay, suggestion.keep, bytesPerLine).savedBytesPerMonth;
                const rate = ruleRate(n);
                const ruled = hasPatternRule(n);
                return (
                  <tr
                    key={`${n.hash}|${n.service}|${n.level}`}
                    className="border-t border-gray-100 dark:border-gray-700/60"
                  >
                    <td
                      className="max-w-[320px] truncate py-2 pr-3 font-mono text-[12.5px]"
                      title={n.pattern || n.sample}
                    >
                      {n.pattern || n.sample || n.hash}
                    </td>
                    <td className="py-2 pr-3">{n.service || 'no service'}</td>
                    <td className="py-2 pr-3 text-right font-mono">{formatCount(n.perDay)}</td>
                    <td className="py-2 pr-3 text-right font-mono">
                      {bytesPerLine > 0 ? formatBytes(n.perDay * bytesPerLine) : 'n/a'}
                    </td>
                    <td className="py-2 pr-3">
                      <TonePill text={suggestion.label} tone={suggestion.tone} />
                    </td>
                    <td className="py-2 pr-3 text-right font-mono">
                      {suggestion.keep === null ? '—' : formatBytes(saves)}
                    </td>
                    <td className="py-2 whitespace-nowrap">
                      {ruled ? (
                        <TonePill
                          text={`rule added · keeps ${Math.round((rate ?? 1) * 100)}%`}
                          tone="neutral"
                        />
                      ) : (
                        suggestion.keep !== null && (
                          <Button
                            size="small"
                            onClick={() =>
                              setRuleFor({
                                keep: suggestion.keep ?? 10,
                                target: {
                                  hash: n.hash,
                                  patternText: n.pattern,
                                  service: n.service,
                                  level: n.level,
                                  perDay: n.perDay,
                                },
                              })
                            }
                          >
                            Apply rule…
                          </Button>
                        )
                      )}
                    </td>
                  </tr>
                );
              })}
              {response && response.noisy.length === 0 && (
                <tr>
                  <td
                    colSpan={7}
                    className="py-8 text-center text-sm text-gray-500 dark:text-gray-400"
                  >
                    No logs in 7 days
                  </td>
                </tr>
              )}
            </tbody>
          </table>
        </div>
        <span className="text-xs text-gray-500 dark:text-gray-400">
          Suggestions by level: debug is dropped, info sampled to 10%, warnings to 25%; they never
          touch error lines. Saves are estimated from the project&apos;s average stored line size.
        </span>
      </Card>

      {ruleFor && (
        <SamplingRuleModalComponent
          projectId={projectId}
          target={ruleFor.target}
          bytesPerLine={bytesPerLine}
          initialKeep={ruleFor.keep}
          onClose={() => setRuleFor(null)}
          onSaved={() => {
            setRuleFor(null);
            loadRules();
          }}
        />
      )}
    </div>
  );
};
