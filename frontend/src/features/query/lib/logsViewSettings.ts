import { useCallback, useEffect, useState } from 'react';

/** Where a clicked line's details open. */
export type DetailPlacement = 'side' | 'below' | 'inline';
export type DetailWidth = 's' | 'm' | 'l';
export type RowDensity = 'comfortable' | 'compact';
export type TextSize = 12 | 13 | 14;
export type LayoutPreset = 'split' | 'wide' | 'focus';

/** The logs page layout, remembered per browser like wrap and columns. */
export interface LogsViewSettings {
  showFields: boolean;
  showChart: boolean;
  detail: DetailPlacement;
  detailWidth: DetailWidth;
  density: RowDensity;
  textSize: TextSize;
}

const STORAGE_KEY = 'obzly-logs-view-v1';

export const DEFAULT_VIEW_SETTINGS: LogsViewSettings = {
  showFields: true,
  showChart: true,
  detail: 'side',
  detailWidth: 'm',
  density: 'comfortable',
  textSize: 13,
};

export const LAYOUT_PRESETS: {
  id: LayoutPreset;
  label: string;
  hint: string;
  patch: Partial<LogsViewSettings>;
}[] = [
  {
    id: 'split',
    label: 'Split',
    hint: 'Fields and chart beside the lines; details open beside them when you click a line',
    patch: { showFields: true, showChart: true, detail: 'side' },
  },
  {
    id: 'wide',
    label: 'Wide',
    hint: 'Lines get the width; details open beside them only when you click a line',
    patch: { showFields: false, showChart: true, detail: 'side' },
  },
  {
    id: 'focus',
    label: 'Focus',
    hint: 'Only the lines, in compact rows; details open inside the clicked row',
    patch: { showFields: false, showChart: false, detail: 'inline', density: 'compact' },
  },
];

/** The preset the settings match, or null for a custom layout. */
export const activePreset = (settings: LogsViewSettings): LayoutPreset | null =>
  LAYOUT_PRESETS.find((preset) =>
    Object.entries(preset.patch).every(
      ([key, value]) => settings[key as keyof LogsViewSettings] === value,
    ),
  )?.id ?? null;

const isOneOf = <T>(value: unknown, options: readonly T[]): value is T =>
  options.includes(value as T);

const readSettings = (): LogsViewSettings => {
  try {
    const raw = localStorage.getItem(STORAGE_KEY);
    if (!raw) return DEFAULT_VIEW_SETTINGS;
    const parsed = JSON.parse(raw) as Partial<Record<keyof LogsViewSettings, unknown>>;
    const d = DEFAULT_VIEW_SETTINGS;
    // Field by field, so one bad value doesn't drop the rest.
    return {
      showFields: typeof parsed.showFields === 'boolean' ? parsed.showFields : d.showFields,
      showChart: typeof parsed.showChart === 'boolean' ? parsed.showChart : d.showChart,
      detail: isOneOf(parsed.detail, ['side', 'below', 'inline'] as const)
        ? parsed.detail
        : d.detail,
      detailWidth: isOneOf(parsed.detailWidth, ['s', 'm', 'l'] as const)
        ? parsed.detailWidth
        : d.detailWidth,
      density: isOneOf(parsed.density, ['comfortable', 'compact'] as const)
        ? parsed.density
        : d.density,
      textSize: isOneOf(parsed.textSize, [12, 13, 14] as const) ? parsed.textSize : d.textSize,
    };
  } catch {
    return DEFAULT_VIEW_SETTINGS;
  }
};

export const useLogsViewSettings = (): {
  settings: LogsViewSettings;
  update: (patch: Partial<LogsViewSettings>) => void;
  reset: () => void;
} => {
  const [settings, setSettings] = useState<LogsViewSettings>(readSettings);

  useEffect(() => {
    try {
      localStorage.setItem(STORAGE_KEY, JSON.stringify(settings));
    } catch {
      /* unavailable - the layout just won't persist */
    }
  }, [settings]);

  const update = useCallback(
    (patch: Partial<LogsViewSettings>) => setSettings((prev) => ({ ...prev, ...patch })),
    [],
  );
  const reset = useCallback(() => setSettings(DEFAULT_VIEW_SETTINGS), []);

  return { settings, update, reset };
};
