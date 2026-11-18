// The admin's theme defaults (Obzly settings › General › Appearance) and how
// they combine with a user's own choice.
import { DEFAULT_THEME_PREFS, type ThemePreference, type ThemePrefs } from './themeStorage';
import { type DarkThemeId, type LightThemeId, isDarkThemeId, isLightThemeId } from './themes';

export interface ThemePolicy {
  defaultMode: ThemePreference;
  defaultLight: LightThemeId;
  defaultDark: DarkThemeId;
  /** Off: everyone gets the default light and dark themes; users still pick the mode. */
  userChoiceAllowed: boolean;
}

/** The theme fields of GET /users/settings. */
export interface ThemePolicySettings {
  defaultThemeMode?: string;
  defaultLightTheme?: string;
  defaultDarkTheme?: string;
  isUserThemeChoiceAllowed?: boolean;
}

export type InitialThemeSync =
  /** The profile has prefs: they win over this browser's. */
  | { kind: 'apply'; prefs: ThemePrefs }
  /** Only this browser has prefs (and they're this user's): save them to the profile once. */
  | { kind: 'upload'; prefs: ThemePrefs }
  /** This browser's prefs belong to someone else: drop them and use the defaults. */
  | { kind: 'defaults' }
  /** Nothing saved anywhere: the admin defaults apply. */
  | { kind: 'none' };

const POLICY_KEY = 'obzly-theme-policy';

export const themePolicyFromSettings = (settings: ThemePolicySettings): ThemePolicy => {
  const mode = settings.defaultThemeMode;
  return {
    defaultMode:
      mode === 'light' || mode === 'dark' || mode === 'system' ? mode : DEFAULT_THEME_PREFS.mode,
    defaultLight: isLightThemeId(settings.defaultLightTheme)
      ? settings.defaultLightTheme
      : DEFAULT_THEME_PREFS.light,
    defaultDark: isDarkThemeId(settings.defaultDarkTheme)
      ? settings.defaultDarkTheme
      : DEFAULT_THEME_PREFS.dark,
    userChoiceAllowed: settings.isUserThemeChoiceAllowed !== false,
  };
};

/** What a user who never picked a theme starts from. */
export const defaultPrefsFor = (policy: ThemePolicy | null): ThemePrefs =>
  policy
    ? {
        mode: policy.defaultMode,
        light: policy.defaultLight,
        dark: policy.defaultDark,
        colorBlindCharts: false,
      }
    : DEFAULT_THEME_PREFS;

/** The prefs that actually apply: the admin's themes win when users may not choose; mode and colour-blind charts stay the user's. */
export const applyThemePolicy = (prefs: ThemePrefs, policy: ThemePolicy | null): ThemePrefs =>
  policy && !policy.userChoiceAllowed
    ? { ...prefs, light: policy.defaultLight, dark: policy.defaultDark }
    : prefs;

export const decideInitialThemeSync = ({
  profilePrefs,
  localPrefs,
  localOwner,
  userId,
}: {
  profilePrefs: ThemePrefs | null;
  localPrefs: ThemePrefs | null;
  localOwner: string | null;
  userId: string;
}): InitialThemeSync => {
  if (profilePrefs) return { kind: 'apply', prefs: profilePrefs };
  if (!localPrefs) return { kind: 'none' };
  // No owner: saved before profile sync existed, so it's this browser's user.
  if (localOwner === null || localOwner === userId) return { kind: 'upload', prefs: localPrefs };
  return { kind: 'defaults' };
};

/** The last policy seen, so a reload starts on the admin's defaults instead of flashing the built-in ones. */
export const getStoredThemePolicy = (): ThemePolicy | null => {
  try {
    const stored = localStorage.getItem(POLICY_KEY);
    return stored ? themePolicyFromSettings(policyToSettings(JSON.parse(stored))) : null;
  } catch {
    return null;
  }
};

export const setStoredThemePolicy = (policy: ThemePolicy): void => {
  try {
    localStorage.setItem(POLICY_KEY, JSON.stringify(policy));
  } catch (error) {
    console.warn('Failed to save theme policy to localStorage:', error);
  }
};

const policyToSettings = (raw: unknown): ThemePolicySettings => {
  if (typeof raw !== 'object' || raw === null) return {};
  const value = raw as Record<string, unknown>;
  return {
    defaultThemeMode: typeof value.defaultMode === 'string' ? value.defaultMode : undefined,
    defaultLightTheme: typeof value.defaultLight === 'string' ? value.defaultLight : undefined,
    defaultDarkTheme: typeof value.defaultDark === 'string' ? value.defaultDark : undefined,
    isUserThemeChoiceAllowed: value.userChoiceAllowed !== false,
  };
};
