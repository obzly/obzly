// The Traces results view (layout preset, panels, columns...), kept per
// browser like the Logs results view.

export type TraceColumnId =
  'trace' | 'root' | 'services' | 'start' | 'duration' | 'spans' | 'errors';
export type TraceDetailPlacement = 'side' | 'below' | 'inline';
export type TraceDetailWidth = 's' | 'm' | 'l';
export type TraceRowDensity = 'comfortable' | 'compact';
export type TraceTextSize = 12 | 13 | 14;
export type TraceLayoutPreset = 'split' | 'wide' | 'focus' | 'custom';

export interface TraceViewSettings {
  preset: TraceLayoutPreset;
  showSummary: boolean;
  detail: TraceDetailPlacement;
  detailWidth: TraceDetailWidth;
  density: TraceRowDensity;
  textSize: TraceTextSize;
  wrap: boolean;
  columns: TraceColumnId[];
}

export const TRACE_COLUMNS: { id: TraceColumnId; label: string }[] = [
  { id: 'trace', label: 'Trace' },
  { id: 'root', label: 'Root service' },
  { id: 'services', label: 'Services' },
  { id: 'start', label: 'Start time' },
  { id: 'duration', label: 'Duration' },
  { id: 'spans', label: 'Spans' },
  { id: 'errors', label: 'Errors' },
];

export const DEFAULT_TRACE_COLUMNS: TraceColumnId[] = TRACE_COLUMNS.map((c) => c.id);

export const TRACE_LAYOUT_PRESETS: {
  id: Exclude<TraceLayoutPreset, 'custom'>;
  label: string;
  hint: string;
  apply: Partial<TraceViewSettings>;
}[] = [
  {
    id: 'split',
    label: 'Split',
    hint: 'Summary on top; the waterfall opens beside the list when you click a trace',
    apply: { showSummary: true, detail: 'side' },
  },
  {
    id: 'wide',
    label: 'Wide',
    hint: 'The list gets the full width; the waterfall opens below it',
    apply: { showSummary: true, detail: 'below' },
  },
  {
    id: 'focus',
    label: 'Focus',
    hint: 'Only the list; the waterfall opens inside the row',
    apply: { showSummary: false, detail: 'inline', density: 'compact' },
  },
];

export const DEFAULT_TRACE_VIEW_SETTINGS: TraceViewSettings = {
  preset: 'split',
  showSummary: true,
  detail: 'side',
  detailWidth: 'm',
  density: 'comfortable',
  textSize: 13,
  wrap: false,
  columns: DEFAULT_TRACE_COLUMNS,
};

const STORAGE_KEY = 'obzly-traces-view-settings';

const oneOf = <T>(value: unknown, allowed: readonly T[], fallback: T): T =>
  allowed.includes(value as T) ? (value as T) : fallback;

/** Reads stored settings, dropping anything unknown (old or hand-edited values). */
export const parseTraceViewSettings = (raw: string | null): TraceViewSettings => {
  const d = DEFAULT_TRACE_VIEW_SETTINGS;
  if (!raw) return d;
  let stored: Partial<Record<keyof TraceViewSettings, unknown>>;
  try {
    stored = JSON.parse(raw) as typeof stored;
  } catch {
    return d;
  }
  if (!stored || typeof stored !== 'object') return d;

  const columns = Array.isArray(stored.columns)
    ? DEFAULT_TRACE_COLUMNS.filter((id) => (stored.columns as unknown[]).includes(id))
    : d.columns;

  return {
    preset: oneOf(stored.preset, ['split', 'wide', 'focus', 'custom'] as const, d.preset),
    showSummary: typeof stored.showSummary === 'boolean' ? stored.showSummary : d.showSummary,
    detail: oneOf(stored.detail, ['side', 'below', 'inline'] as const, d.detail),
    detailWidth: oneOf(stored.detailWidth, ['s', 'm', 'l'] as const, d.detailWidth),
    density: oneOf(stored.density, ['comfortable', 'compact'] as const, d.density),
    textSize: oneOf(stored.textSize, [12, 13, 14] as const, d.textSize),
    wrap: typeof stored.wrap === 'boolean' ? stored.wrap : d.wrap,
    columns: columns.length > 0 ? columns : d.columns,
  };
};

export const readTraceViewSettings = (): TraceViewSettings => {
  try {
    return parseTraceViewSettings(localStorage.getItem(STORAGE_KEY));
  } catch {
    return DEFAULT_TRACE_VIEW_SETTINGS;
  }
};

export const writeTraceViewSettings = (settings: TraceViewSettings): void => {
  try {
    localStorage.setItem(STORAGE_KEY, JSON.stringify(settings));
  } catch {
    // Private mode or a full quota: the view still works, it just isn't kept.
  }
};
