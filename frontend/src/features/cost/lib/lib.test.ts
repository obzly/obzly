import { describe, expect, it } from 'vitest';

import type { ExploreResponse } from '../../../entity/cost';
import {
  activeDimensions,
  drillNext,
  exploreCsvRows,
  scopeLine,
  setFilterValues,
  valueLabel,
} from './explorer';
import { formatChange, formatMoney } from './format';
import { mergeCoverage, mergeExplore, mergeFacets, mergeSummary } from './merge';
import { allowedGranularities, fitGranularity, resolveRange, spanDays } from './ranges';

const site = (siteId: string) => ({ siteId, siteName: siteId });

describe('resolveRange', () => {
  it('turns presets into whole UTC days', () => {
    expect(resolveRange('mtd', '2026-11-10')).toEqual({ from: '2026-11-01', to: '2026-11-10' });
    expect(resolveRange('last30', '2026-11-10')).toEqual({ from: '2026-10-12', to: '2026-11-10' });
    expect(resolveRange('lastMonth', '2026-03-15')).toEqual({
      from: '2026-02-01',
      to: '2026-02-28',
    });
    expect(resolveRange('last6m', '2026-02-10')).toEqual({ from: '2025-09-01', to: '2026-02-10' });
    expect(resolveRange('lastYear', '2026-11-10')).toEqual({
      from: '2025-01-01',
      to: '2025-12-31',
    });
  });

  it('offers daily periods only up to 92 days', () => {
    expect(spanDays({ from: '2026-11-01', to: '2026-11-01' })).toBe(1);
    expect(allowedGranularities(resolveRange('last90', '2026-11-10'))).toContain('daily');
    expect(allowedGranularities(resolveRange('last6m', '2026-11-10'))).not.toContain('daily');
    expect(fitGranularity(resolveRange('last6m', '2026-11-10'), 'daily')).toBe('monthly');
    expect(fitGranularity(resolveRange('mtd', '2026-11-10'))).toBe('daily');
    expect(fitGranularity(resolveRange('ytd', '2026-11-10'), 'quarterly')).toBe('quarterly');
  });
});

describe('format', () => {
  it('formats money in its currency and falls back for unknown codes', () => {
    expect(formatMoney(1234.5, 'USD')).toContain('1,234.50');
    expect(formatMoney(3, 'XX1')).toBe('3.00 XX1');
    expect(formatChange(110, 100)).toBe('+10.0%');
    expect(formatChange(5, 0)).toBe('');
  });
});

describe('merging Sites', () => {
  it('reports full coverage only when every Site in scope had it', () => {
    expect(mergeCoverage(['full', 'full', ''])).toBe('full');
    expect(mergeCoverage(['full', 'none'])).toBe('partial');
    expect(mergeCoverage(['none', ''])).toBe('none');
    expect(mergeCoverage(['', ''])).toBe('');
  });

  it('adds rows per currency, keeps currencies apart and skips failed Sites', () => {
    const periods = [
      { key: '2026-10', from: '2026-10-01', to: '2026-10-31', coverage: 'full' as const },
      { key: '2026-11', from: '2026-11-01', to: '2026-11-10', coverage: 'full' as const },
    ];
    const a: ExploreResponse = {
      periods,
      currencies: [
        {
          currency: 'USD',
          rows: [{ key: 'AmazonEC2', values: [10, 5], total: 15 }],
          totals: [10, 5],
          total: 15,
        },
      ],
    };
    const b: ExploreResponse = {
      periods: periods.map((period) => ({ ...period, coverage: 'partial' as const })),
      currencies: [
        {
          currency: 'USD',
          rows: [
            { key: 'AmazonS3', values: [30, 0], total: 30 },
            { key: 'AmazonEC2', values: [1, 1], total: 2 },
          ],
          totals: [31, 1],
          total: 32,
        },
        {
          currency: 'EUR',
          rows: [{ key: 'Storage', values: [2, 2], total: 4 }],
          totals: [2, 2],
          total: 4,
        },
      ],
    };
    const merged = mergeExplore([
      { site: site('a'), value: a },
      { site: site('b'), value: b },
      { site: site('c'), error: 'unavailable' },
    ]);
    expect(merged.periods.map((period) => period.coverage)).toEqual(['partial', 'partial']);
    expect(merged.currencies.map((block) => block.currency)).toEqual(['USD', 'EUR']);
    const usd = merged.currencies[0];
    expect(usd.rows.map((row) => [row.key, row.total])).toEqual([
      ['AmazonS3', 30],
      ['AmazonEC2', 17],
    ]);
    expect(usd.rows[1].values).toEqual([11, 6]);
    expect(usd.totals).toEqual([41, 6]);
    expect(a.currencies[0].rows[0].values).toEqual([10, 5]);
  });

  it('merges facets and summaries, with a forecast only when every Site has one', () => {
    const facets = mergeFacets([
      {
        site: site('a'),
        value: { dimensions: { region: [{ value: 'eu-west-1', amounts: { USD: 3 } }] } },
      },
      {
        site: site('b'),
        value: {
          dimensions: {
            region: [
              { value: 'eu-west-1', amounts: { USD: 2, EUR: 1 } },
              { value: 'westeurope', amounts: { EUR: 9 } },
            ],
          },
        },
      },
    ]);
    expect(facets.dimensions.region).toEqual([
      { value: 'westeurope', amounts: { EUR: 9 } },
      { value: 'eu-west-1', amounts: { USD: 5, EUR: 1 } },
    ]);

    const summary = (forecast?: number) => ({
      today: '2026-11-10',
      currencies: [
        {
          currency: 'USD',
          monthToDate: 100,
          lastMonth: 300,
          lastMonthSameDays: 90,
          forecast,
          daily: [{ day: '2026-11-09', amount: 10, estimated: true }],
        },
      ],
      sources: [],
      openAnomalies: 1,
    });
    const both = mergeSummary([
      { site: site('a'), value: summary(310) },
      { site: site('b'), value: summary(290) },
    ]);
    expect(both.currencies[0].forecast).toBe(600);
    expect(both.currencies[0].daily).toEqual([{ day: '2026-11-09', amount: 20, estimated: true }]);
    expect(both.openAnomalies).toBe(2);
    const one = mergeSummary([
      { site: site('a'), value: summary(310) },
      { site: site('b'), value: summary() },
    ]);
    expect(one.currencies[0].forecast).toBeUndefined();
  });
});

describe('explorer filters', () => {
  it('sets and clears dimension and tag filters', () => {
    let filters = setFilterValues({}, 'provider', ['azure']);
    filters = setFilterValues(filters, 'tag:team', ['payments']);
    expect(filters).toEqual({ provider: ['azure'], tags: { team: ['payments'] } });
    expect(activeDimensions(filters)).toEqual(['provider', 'tag:team']);
    expect(scopeLine(filters, 'tag:team')).toBe('Provider: Azure');
    filters = setFilterValues(filters, 'tag:team', []);
    filters = setFilterValues(filters, 'provider', []);
    expect(filters).toEqual({});
  });

  it('drills from the broad to the single resource and labels values', () => {
    expect(drillNext('provider')).toBe('account');
    expect(drillNext('service')).toBe('resource');
    expect(drillNext('resource')).toBeUndefined();
    expect(drillNext('tag:env')).toBe('service');
    expect(valueLabel('account', '111122223333', 'Contoso-Prod')).toBe(
      'Contoso-Prod (111122223333)',
    );
    expect(valueLabel('tag:env', '')).toBe('(untagged)');
  });

  it('exports one currency as CSV rows', () => {
    const rows = exploreCsvRows(
      {
        periods: [{ key: '2026-11', from: '2026-11-01', to: '2026-11-10', coverage: 'full' }],
        currencies: [
          {
            currency: 'USD',
            rows: [{ key: 'aws', values: [1.234], total: 1.234 }],
            other: { key: '', label: '3 more', values: [1], total: 1 },
            totals: [2.234],
            total: 2.234,
          },
        ],
      },
      'USD',
      'provider',
    );
    expect(rows).toEqual([
      ['Provider', '2026-11', 'Total', 'Currency'],
      ['AWS', 1.23, 1.23, 'USD'],
      ['3 more', 1, 1, 'USD'],
      ['Total', 2.23, 2.23, 'USD'],
    ]);
  });
});
