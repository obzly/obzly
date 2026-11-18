import { describe, expect, it } from 'vitest';

import {
  applyThemePolicy,
  decideInitialThemeSync,
  defaultPrefsFor,
  themePolicyFromSettings,
} from './themePolicy';
import { DEFAULT_THEME_PREFS, type ThemePrefs } from './themeStorage';

const mine: ThemePrefs = {
  mode: 'dark',
  light: 'soft-light',
  dark: 'midnight',
  colorBlindCharts: true,
};

describe('themePolicyFromSettings', () => {
  it('reads the admin defaults', () => {
    expect(
      themePolicyFromSettings({
        defaultThemeMode: 'dark',
        defaultLightTheme: 'semi-dark',
        defaultDarkTheme: 'black',
        isUserThemeChoiceAllowed: false,
      }),
    ).toEqual({
      defaultMode: 'dark',
      defaultLight: 'semi-dark',
      defaultDark: 'black',
      userChoiceAllowed: false,
    });
  });

  it('falls back to the built-in defaults for missing or mismatched values', () => {
    expect(
      themePolicyFromSettings({
        defaultThemeMode: 'sepia',
        defaultLightTheme: 'black',
        defaultDarkTheme: 'no-such',
      }),
    ).toEqual({
      defaultMode: 'system',
      defaultLight: 'emerald-light',
      defaultDark: 'emerald-dark',
      userChoiceAllowed: true,
    });
  });
});

describe('defaultPrefsFor', () => {
  it('starts a user with no saved prefs from the admin defaults', () => {
    const policy = themePolicyFromSettings({
      defaultThemeMode: 'light',
      defaultLightTheme: 'soft-light',
      defaultDarkTheme: 'black',
    });
    expect(defaultPrefsFor(policy)).toEqual({
      mode: 'light',
      light: 'soft-light',
      dark: 'black',
      colorBlindCharts: false,
    });
  });

  it('uses the built-in defaults before settings load', () => {
    expect(defaultPrefsFor(null)).toEqual(DEFAULT_THEME_PREFS);
  });
});

describe('applyThemePolicy', () => {
  const forced = themePolicyFromSettings({
    defaultLightTheme: 'semi-dark',
    defaultDarkTheme: 'black',
    isUserThemeChoiceAllowed: false,
  });

  it('keeps the user’s themes when they may choose', () => {
    expect(applyThemePolicy(mine, { ...forced, userChoiceAllowed: true })).toEqual(mine);
    expect(applyThemePolicy(mine, null)).toEqual(mine);
  });

  it('forces the admin themes but keeps the user’s mode and colour-blind charts', () => {
    expect(applyThemePolicy(mine, forced)).toEqual({
      mode: 'dark',
      light: 'semi-dark',
      dark: 'black',
      colorBlindCharts: true,
    });
  });
});

describe('decideInitialThemeSync', () => {
  const local: ThemePrefs = { ...DEFAULT_THEME_PREFS, mode: 'light' };

  it('lets profile prefs win over this browser’s', () => {
    expect(
      decideInitialThemeSync({
        profilePrefs: mine,
        localPrefs: local,
        localOwner: 'u1',
        userId: 'u1',
      }),
    ).toEqual({ kind: 'apply', prefs: mine });
  });

  it('uploads this browser’s prefs once when the profile has none', () => {
    expect(
      decideInitialThemeSync({
        profilePrefs: null,
        localPrefs: local,
        localOwner: null,
        userId: 'u1',
      }),
    ).toEqual({ kind: 'upload', prefs: local });
    expect(
      decideInitialThemeSync({
        profilePrefs: null,
        localPrefs: local,
        localOwner: 'u1',
        userId: 'u1',
      }),
    ).toEqual({ kind: 'upload', prefs: local });
  });

  it('never copies another user’s browser prefs into this profile', () => {
    expect(
      decideInitialThemeSync({
        profilePrefs: null,
        localPrefs: local,
        localOwner: 'u2',
        userId: 'u1',
      }),
    ).toEqual({ kind: 'defaults' });
  });

  it('does nothing when no prefs are saved anywhere', () => {
    expect(
      decideInitialThemeSync({
        profilePrefs: null,
        localPrefs: null,
        localOwner: null,
        userId: 'u1',
      }),
    ).toEqual({ kind: 'none' });
  });
});
