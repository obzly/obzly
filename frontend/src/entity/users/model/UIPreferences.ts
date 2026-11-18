import type { ThemePrefs } from '../../../shared/theme';

/** Per-user UI choices saved to the profile (PUT /users/me/preferences). */
export interface UIPreferences {
  theme?: ThemePrefs;
}
