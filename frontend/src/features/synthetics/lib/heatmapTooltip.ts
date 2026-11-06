import { escapeHtml } from '../../../shared/lib';

// Location names come from ingested synthetics_* labels, which anyone with the
// project's ingest key can set, and ECharts renders the returned string as HTML.
export const heatmapTooltip =
  (names: string[], hours: number[]) =>
  (params: unknown): string => {
    const [hour, row, value] = (params as { value: number[] }).value;
    return `${escapeHtml(names[row] ?? '')} · ${new Date(hours[hour]).toLocaleString()}<br/>${value}% of runs passed`;
  };
