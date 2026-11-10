// A control plane asks every Site and adds the answers up; a Site that fails is reported beside
// the result, never counted as nothing.
import type {
  CoverageRow,
  Finding,
  PostureFacets,
  PostureSummary,
  Severity,
  StandardScore,
  TrendsResponse,
} from '../../../entity/posture';
import type { CostSite, SiteOutcome } from '../../cost';

export const SEVERITIES: Severity[] = ['critical', 'high', 'medium', 'low', 'informational'];

export const SEVERITY_COLOR: Record<Severity, string> = {
  critical: '#be123c',
  high: '#dc2626',
  medium: '#f59e0b',
  low: '#3b82f6',
  informational: '#9ca3af',
};

export const SEVERITY_LABEL: Record<Severity, string> = {
  critical: 'Critical',
  high: 'High',
  medium: 'Medium',
  low: 'Low',
  informational: 'Info',
};

export interface SiteFinding extends Finding, CostSite {}

export interface SiteCoverageRow extends CoverageRow, CostSite {}

const succeeded = <T>(outcomes: SiteOutcome<T>[]): T[] =>
  outcomes.flatMap((outcome) => (outcome.value === undefined ? [] : [outcome.value]));

const percent = (part: number, whole: number) =>
  whole ? Math.round((part / whole) * 1000) / 10 : 0;

export const mergeSummary = (outcomes: SiteOutcome<PostureSummary>[]): PostureSummary => {
  const summaries = succeeded(outcomes);
  const standards = new Map<string, StandardScore & { trendSums: Map<string, number[]> }>();
  const merged: PostureSummary = {
    standards: [],
    open: {},
    suppressed: 0,
    resolved30d: 0,
    coverage: { native: 0, fallback: 0, baseline: 0, notCovered: 0, stale: 0 },
  };
  for (const summary of summaries) {
    for (const [severity, count] of Object.entries(summary.open)) {
      merged.open[severity as Severity] = (merged.open[severity as Severity] ?? 0) + (count ?? 0);
    }
    merged.suppressed += summary.suppressed;
    merged.resolved30d += summary.resolved30d;
    for (const key of Object.keys(merged.coverage) as (keyof PostureSummary['coverage'])[]) {
      merged.coverage[key] += summary.coverage[key];
    }
    if (summary.lastScanAt && (!merged.lastScanAt || summary.lastScanAt > merged.lastScanAt)) {
      merged.lastScanAt = summary.lastScanAt;
    }
    for (const score of summary.standards) {
      const existing = standards.get(score.standard);
      if (!existing) {
        standards.set(score.standard, {
          ...score,
          sources: [...score.sources],
          trendSums: new Map(),
        });
      } else {
        existing.passed += score.passed;
        existing.failed += score.failed;
        existing.evaluated += score.evaluated;
        existing.sources = [...new Set([...existing.sources, ...score.sources])].sort();
      }
      const target = standards.get(score.standard)!;
      for (const point of score.trend ?? []) {
        target.trendSums.set(point.day, [
          ...(target.trendSums.get(point.day) ?? []),
          point.percent,
        ]);
      }
    }
  }
  merged.standards = [...standards.values()].map(({ trendSums, ...score }) => ({
    ...score,
    percent: percent(score.passed, score.evaluated),
    // Sites' percents for a day, averaged: their control counts aren't in the trend.
    trend: [...trendSums.entries()]
      .sort(([a], [b]) => a.localeCompare(b))
      .map(([day, values]) => ({
        day,
        percent: Math.round((values.reduce((a, b) => a + b, 0) / values.length) * 10) / 10,
      })),
  }));
  return merged;
};

const severityIndex = (severity: Severity) => SEVERITIES.indexOf(severity);

/** Findings from every Site, most severe and most recent first. */
export const mergeFindings = (
  outcomes: SiteOutcome<{ total: number; findings: Finding[] }>[],
): { total: number; findings: SiteFinding[] } => ({
  total: succeeded(outcomes).reduce((sum, response) => sum + response.total, 0),
  findings: outcomes
    .flatMap((outcome) =>
      (outcome.value?.findings ?? []).map((finding) => ({ ...finding, ...outcome.site })),
    )
    .sort(
      (a, b) =>
        severityIndex(a.severity) - severityIndex(b.severity) ||
        b.lastSeen.localeCompare(a.lastSeen),
    ),
});

export const mergeFacets = (outcomes: SiteOutcome<PostureFacets>[]): PostureFacets => {
  const dimensions: Record<
    string,
    Map<string, { value: string; label?: string; count: number }>
  > = {};
  for (const response of succeeded(outcomes)) {
    for (const [dimension, values] of Object.entries(response.dimensions)) {
      const merged = (dimensions[dimension] ??= new Map());
      for (const value of values) {
        const existing = merged.get(value.value);
        if (existing) existing.count += value.count;
        else merged.set(value.value, { ...value });
      }
    }
  }
  return {
    dimensions: Object.fromEntries(
      Object.entries(dimensions).map(([dimension, values]) => [
        dimension,
        [...values.values()].sort((a, b) => b.count - a.count),
      ]),
    ),
  };
};

export const mergeTrends = (outcomes: SiteOutcome<TrendsResponse>[]): TrendsResponse => {
  const responses = succeeded(outcomes);
  if (responses.length === 0) return { periods: [] };
  return {
    periods: responses[0].periods.map((first, index) => {
      const periods = responses.map((response) => response.periods[index]).filter(Boolean);
      const open: Partial<Record<Severity, number>> = {};
      const scores: Record<string, number[]> = {};
      for (const period of periods) {
        for (const [severity, count] of Object.entries(period.open)) {
          open[severity as Severity] = (open[severity as Severity] ?? 0) + (count ?? 0);
        }
        for (const [standard, value] of Object.entries(period.scores)) {
          (scores[standard] ??= []).push(value);
        }
      }
      return {
        ...first,
        open,
        opened: periods.reduce((sum, period) => sum + period.opened, 0),
        resolved: periods.reduce((sum, period) => sum + period.resolved, 0),
        scores: Object.fromEntries(
          Object.entries(scores).map(([standard, values]) => [
            standard,
            Math.round((values.reduce((a, b) => a + b, 0) / values.length) * 10) / 10,
          ]),
        ),
      };
    }),
  };
};

export const mergeCoverage = (outcomes: SiteOutcome<CoverageRow[]>[]): SiteCoverageRow[] =>
  outcomes.flatMap((outcome) => (outcome.value ?? []).map((row) => ({ ...row, ...outcome.site })));

/** Fills a baseline check's {name}, {region} and {resourceGroup} from the finding. */
export const fillRemediation = (text: string, finding: Finding): string => {
  const values: Record<string, string> = {
    name: finding.evidence.name || finding.resourceKey.split(/[/:]/).pop() || '<name>',
    region: finding.region === 'global' ? '<region>' : finding.region,
    resourceGroup: finding.evidence.resourceGroup || '<resource group>',
  };
  return text.replace(/\{(name|region|resourceGroup)\}/g, (_, key: string) => values[key]);
};

/** "Native", "Obzly baseline" or "Mixed" for a standard's sources. */
export const sourcesLabel = (sources: string[]): string =>
  sources.length > 1 ? 'Mixed' : sources[0] === 'baseline' ? 'Obzly baseline' : 'Native';
