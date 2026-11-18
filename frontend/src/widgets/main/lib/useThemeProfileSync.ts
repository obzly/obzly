// Keeps the theme in step with the signed-in user's profile and the admin's
// Appearance defaults.
import { useEffect } from 'react';

import { type UserProfile, type UsersSettings, userApi } from '../../../entity/users';
import {
  type ThemePrefs,
  decideInitialThemeSync,
  getStoredThemeOwner,
  getStoredThemePrefsOrNull,
  parseThemePrefs,
  setStoredThemeOwner,
  themePolicyFromSettings,
  useTheme,
} from '../../../shared/theme';

const SAVE_DELAY_MS = 500;

export const useThemeProfileSync = (
  user: UserProfile | undefined,
  settings: UsersSettings | undefined,
): void => {
  const { setPolicy, applySavedPrefs, onUserPrefsChange, setSavedToProfile } = useTheme();

  useEffect(() => {
    if (settings) setPolicy(themePolicyFromSettings(settings));
  }, [settings, setPolicy]);

  useEffect(() => {
    if (!user) return;

    const decision = decideInitialThemeSync({
      profilePrefs: parseThemePrefs(user.uiPreferences?.theme),
      localPrefs: getStoredThemePrefsOrNull(),
      localOwner: getStoredThemeOwner(),
      userId: user.id,
    });
    if (decision.kind === 'apply') applySavedPrefs(decision.prefs);
    if (decision.kind === 'defaults') applySavedPrefs(null);
    if (decision.kind === 'upload') saveToProfile(decision.prefs);
    setStoredThemeOwner(user.id);
    setSavedToProfile(true);

    // Only the user's own changes reach here, so applying profile prefs never saves them back.
    let timer: ReturnType<typeof setTimeout> | undefined;
    const unsubscribe = onUserPrefsChange((prefs) => {
      clearTimeout(timer);
      timer = setTimeout(() => saveToProfile(prefs), SAVE_DELAY_MS);
    });
    return () => {
      unsubscribe();
      clearTimeout(timer);
      setSavedToProfile(false);
    };
  }, [user?.id]);
};

const saveToProfile = (prefs: ThemePrefs) => {
  userApi.updatePreferences({ theme: prefs }).catch((error: unknown) => {
    console.warn('Saving the theme to your profile failed:', error);
  });
};
