import { describe, expect, it } from 'vitest';

import {
  DEFAULT_THEME_PREFS,
  getLinkThemeOverride,
  parseThemePrefs,
  resolveThemeId,
} from './themeStorage';
import { THEMES, chartPalette } from './themes';

describe('resolveThemeId', () => {
  const prefs = { ...DEFAULT_THEME_PREFS, light: 'soft-light', dark: 'midnight' } as const;

  it('follows the system scheme in system mode', () => {
    expect(resolveThemeId({ ...prefs, mode: 'system' }, 'dark', null)).toBe('midnight');
    expect(resolveThemeId({ ...prefs, mode: 'system' }, 'light', null)).toBe('soft-light');
  });

  it('ignores the system scheme in light or dark mode', () => {
    expect(resolveThemeId({ ...prefs, mode: 'light' }, 'dark', null)).toBe('soft-light');
    expect(resolveThemeId({ ...prefs, mode: 'dark' }, 'light', null)).toBe('midnight');
  });

  it('lets a ?theme= link win over the saved choice', () => {
    expect(resolveThemeId({ ...prefs, mode: 'light' }, 'light', 'black')).toBe('black');
  });
});

describe('parseThemePrefs', () => {
  it('rejects anything without a valid mode', () => {
    expect(parseThemePrefs(null)).toBeNull();
    expect(parseThemePrefs({ mode: 'sepia' })).toBeNull();
  });

  it('falls back to defaults for unknown or mismatched theme ids', () => {
    expect(parseThemePrefs({ mode: 'dark', light: 'midnight', dark: 'no-such' })).toEqual({
      ...DEFAULT_THEME_PREFS,
      mode: 'dark',
    });
  });
});

describe('getLinkThemeOverride', () => {
  it('accepts known theme ids only', () => {
    expect(getLinkThemeOverride('?theme=black&kiosk=1')).toBe('black');
    expect(getLinkThemeOverride('?theme=<script>')).toBeNull();
    expect(getLinkThemeOverride('')).toBeNull();
  });
});

describe('chartPalette', () => {
  it('swaps to the colour-blind safe palette for the theme scheme', () => {
    expect(chartPalette(THEMES.midnight, false)[0]).toBe('#5b8cff');
    expect(chartPalette(THEMES.midnight, true)).not.toEqual(
      chartPalette(THEMES['soft-light'], true),
    );
  });
});
