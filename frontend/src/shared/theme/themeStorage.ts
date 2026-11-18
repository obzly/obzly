import {
  type DarkThemeId,
  type LightThemeId,
  type ThemeId,
  isDarkThemeId,
  isLightThemeId,
  isThemeId,
} from './themes';

/** The resolved light/dark scheme, kept under its old name for existing callers. */
export type ThemeMode = 'light' | 'dark';
export type ThemePreference = 'light' | 'dark' | 'system';

export interface ThemePrefs {
  mode: ThemePreference;
  light: LightThemeId;
  dark: DarkThemeId;
  colorBlindCharts: boolean;
}

export const DEFAULT_THEME_PREFS: ThemePrefs = {
  mode: 'system',
  light: 'emerald-light',
  dark: 'emerald-dark',
  colorBlindCharts: false,
};

const PREFS_KEY = 'obzly-theme-prefs';
// Before themes, this held just 'light' or 'dark'.
const LEGACY_KEY = 'obzly-theme';
const OWNER_KEY = 'obzly-theme-prefs-user';

export const parseThemePrefs = (raw: unknown): ThemePrefs | null => {
  if (typeof raw !== 'object' || raw === null) return null;
  const value = raw as Record<string, unknown>;
  const mode = value.mode;
  if (mode !== 'light' && mode !== 'dark' && mode !== 'system') return null;
  return {
    mode,
    light: isLightThemeId(value.light) ? value.light : DEFAULT_THEME_PREFS.light,
    dark: isDarkThemeId(value.dark) ? value.dark : DEFAULT_THEME_PREFS.dark,
    colorBlindCharts: value.colorBlindCharts === true,
  };
};

/** The prefs saved in this browser, or null when the user never picked any here. */
export const getStoredThemePrefsOrNull = (): ThemePrefs | null => {
  try {
    const stored = localStorage.getItem(PREFS_KEY);
    if (stored) {
      const parsed = parseThemePrefs(JSON.parse(stored));
      if (parsed) return parsed;
    }
    const legacy = localStorage.getItem(LEGACY_KEY);
    if (legacy === 'light' || legacy === 'dark') {
      return { ...DEFAULT_THEME_PREFS, mode: legacy };
    }
  } catch (error) {
    console.warn('Failed to read theme from localStorage:', error);
  }
  return null;
};

export const getStoredThemePrefs = (): ThemePrefs =>
  getStoredThemePrefsOrNull() ?? DEFAULT_THEME_PREFS;

export const setStoredThemePrefs = (prefs: ThemePrefs): void => {
  try {
    localStorage.setItem(PREFS_KEY, JSON.stringify(prefs));
    localStorage.removeItem(LEGACY_KEY);
  } catch (error) {
    console.warn('Failed to save theme to localStorage:', error);
  }
};

export const clearStoredThemePrefs = (): void => {
  try {
    localStorage.removeItem(PREFS_KEY);
    localStorage.removeItem(LEGACY_KEY);
  } catch (error) {
    console.warn('Failed to clear theme in localStorage:', error);
  }
};

/** Which signed-in user the browser's prefs were last synced with, so they never leak to the next user. */
export const getStoredThemeOwner = (): string | null => {
  try {
    return localStorage.getItem(OWNER_KEY);
  } catch {
    return null;
  }
};

export const setStoredThemeOwner = (userId: string): void => {
  try {
    localStorage.setItem(OWNER_KEY, userId);
  } catch (error) {
    console.warn('Failed to save theme owner to localStorage:', error);
  }
};

/** `?theme=black` forces a theme for this screen only (wall screens, kiosks); it is never saved. */
export const getLinkThemeOverride = (search: string): ThemeId | null => {
  const value = new URLSearchParams(search).get('theme');
  return isThemeId(value) ? value : null;
};

export const resolveThemeId = (
  prefs: ThemePrefs,
  systemScheme: ThemeMode,
  linkOverride: ThemeId | null,
): ThemeId => {
  if (linkOverride) return linkOverride;
  if (prefs.mode === 'light') return prefs.light;
  if (prefs.mode === 'dark') return prefs.dark;
  return systemScheme === 'dark' ? prefs.dark : prefs.light;
};

export const getSystemScheme = (): ThemeMode =>
  typeof window !== 'undefined' && window.matchMedia?.('(prefers-color-scheme: dark)').matches
    ? 'dark'
    : 'light';
