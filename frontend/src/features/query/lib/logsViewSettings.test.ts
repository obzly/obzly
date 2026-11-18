import { describe, expect, it } from 'vitest';

import { DEFAULT_VIEW_SETTINGS, LAYOUT_PRESETS, activePreset } from './logsViewSettings';

describe('activePreset', () => {
  it('defaults to Split', () => {
    expect(activePreset(DEFAULT_VIEW_SETTINGS)).toBe('split');
  });

  it('matches every preset after applying it', () => {
    for (const preset of LAYOUT_PRESETS) {
      expect(activePreset({ ...DEFAULT_VIEW_SETTINGS, ...preset.patch })).toBe(preset.id);
    }
  });

  it('is custom once a layout control departs from every preset', () => {
    expect(activePreset({ ...DEFAULT_VIEW_SETTINGS, showChart: false })).toBeNull();
    expect(activePreset({ ...DEFAULT_VIEW_SETTINGS, detail: 'below' })).toBeNull();
  });

  it('ignores row and text size outside Focus', () => {
    expect(activePreset({ ...DEFAULT_VIEW_SETTINGS, density: 'compact', textSize: 14 })).toBe(
      'split',
    );
  });
});
