import { createContext } from 'react';

import type { ThemePolicy } from './themePolicy';
import type { ThemeMode, ThemePrefs } from './themeStorage';
import type { StatusColors, ThemeDefinition, ThemeId } from './themes';

export interface ThemeContextValue {
  /** Resolved light/dark scheme of the active theme. */
  theme: ThemeMode;
  themeId: ThemeId;
  definition: ThemeDefinition;
  /** The prefs that apply (the admin's themes when users may not choose). */
  prefs: ThemePrefs;
  /** The user's own choice, before the admin policy; this is what's saved to the profile. */
  userPrefs: ThemePrefs;
  setPrefs: (next: ThemePrefs) => void;
  /** Merges onto the user's own prefs. */
  updatePrefs: (patch: Partial<ThemePrefs>) => void;
  /** Back to the admin defaults (or the built-in ones before settings load). */
  resetPrefs: () => void;
  /** Applies prefs read from the profile (null: none) without counting as a user change. */
  applySavedPrefs: (prefs: ThemePrefs | null) => void;
  /** Called after every change the user makes (not after applySavedPrefs); returns an unsubscribe. */
  onUserPrefsChange: (listener: (prefs: ThemePrefs) => void) => () => void;
  policy: ThemePolicy | null;
  setPolicy: (policy: ThemePolicy) => void;
  /** False when the admin set the themes for everyone. */
  userChoiceAllowed: boolean;
  /** True once changes are saved to the signed-in user's profile, not just this browser. */
  savedToProfile: boolean;
  setSavedToProfile: (saved: boolean) => void;
  /** Theme forced by a `?theme=` link; prefs apply again once it's cleared. */
  linkOverride: ThemeId | null;
  clearLinkOverride: () => void;
  /** Chart series colours (the colour-blind safe set when the user asked for it). */
  chartColors: string[];
  status: StatusColors;
  /** Flips between the user's light and dark theme. */
  toggleTheme: () => void;
}

export const ThemeContext = createContext<ThemeContextValue | null>(null);
