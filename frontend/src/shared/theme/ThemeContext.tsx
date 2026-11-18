import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState } from 'react';
import type { JSX, ReactNode } from 'react';

import { ThemeContext, type ThemeContextValue } from './themeContextValue';
import {
  type ThemePolicy,
  applyThemePolicy,
  defaultPrefsFor,
  getStoredThemePolicy,
  setStoredThemePolicy,
} from './themePolicy';
import {
  type ThemeMode,
  type ThemePrefs,
  clearStoredThemePrefs,
  getLinkThemeOverride,
  getStoredThemePrefsOrNull,
  getSystemScheme,
  resolveThemeId,
  setStoredThemePrefs,
} from './themeStorage';
import { THEMES, chartPalette, statusColors } from './themes';

export function ThemeProvider({ children }: { children: ReactNode }): JSX.Element {
  // Null: the user never picked a theme, so the admin defaults apply.
  const [ownPrefs, setOwnPrefs] = useState<ThemePrefs | null>(getStoredThemePrefsOrNull);
  const [policy, setPolicyState] = useState<ThemePolicy | null>(getStoredThemePolicy);
  const [savedToProfile, setSavedToProfile] = useState(false);
  const [systemScheme, setSystemScheme] = useState<ThemeMode>(getSystemScheme);
  const [linkOverride, setLinkOverride] = useState(() =>
    typeof window === 'undefined' ? null : getLinkThemeOverride(window.location.search),
  );
  const listenersRef = useRef(new Set<(prefs: ThemePrefs) => void>());

  const applySavedPrefs = useCallback((saved: ThemePrefs | null) => {
    setOwnPrefs(saved);
    if (saved) setStoredThemePrefs(saved);
    else clearStoredThemePrefs();
  }, []);

  const setPolicy = useCallback((next: ThemePolicy) => {
    setPolicyState(next);
    setStoredThemePolicy(next);
  }, []);

  const onUserPrefsChange = useCallback((listener: (prefs: ThemePrefs) => void) => {
    listenersRef.current.add(listener);
    return () => {
      listenersRef.current.delete(listener);
    };
  }, []);

  useEffect(() => {
    const query = window.matchMedia?.('(prefers-color-scheme: dark)');
    if (!query) return;
    const onChange = () => setSystemScheme(query.matches ? 'dark' : 'light');
    query.addEventListener('change', onChange);
    return () => query.removeEventListener('change', onChange);
  }, []);

  const userPrefs = ownPrefs ?? defaultPrefsFor(policy);
  const prefs = applyThemePolicy(userPrefs, policy);
  const themeId = resolveThemeId(prefs, systemScheme, linkOverride);
  const definition = THEMES[themeId];

  // Before paint, so a reload doesn't flash the wrong theme.
  useLayoutEffect(() => {
    const root = document.documentElement;
    // Tailwind's `dark:` keys off this class; index.css keys theme colours off data-theme.
    root.classList.toggle('dark', definition.scheme === 'dark');
    root.dataset.theme = themeId;
    root.style.colorScheme = definition.scheme;
  }, [themeId, definition.scheme]);

  const value = useMemo<ThemeContextValue>(() => {
    const setPrefs = (next: ThemePrefs) => {
      setOwnPrefs(next);
      setStoredThemePrefs(next);
      listenersRef.current.forEach((listener) => listener(next));
    };
    const updatePrefs = (patch: Partial<ThemePrefs>) => setPrefs({ ...userPrefs, ...patch });
    return {
      theme: definition.scheme,
      themeId,
      definition,
      prefs,
      userPrefs,
      setPrefs,
      updatePrefs,
      resetPrefs: () => setPrefs(defaultPrefsFor(policy)),
      applySavedPrefs,
      onUserPrefsChange,
      policy,
      setPolicy,
      userChoiceAllowed: policy?.userChoiceAllowed ?? true,
      savedToProfile,
      setSavedToProfile,
      linkOverride,
      clearLinkOverride: () => {
        setLinkOverride(null);
        const url = new URL(window.location.href);
        url.searchParams.delete('theme');
        window.history.replaceState(window.history.state, '', url.toString());
      },
      chartColors: chartPalette(definition, prefs.colorBlindCharts),
      status: statusColors(definition.scheme),
      toggleTheme: () => updatePrefs({ mode: definition.scheme === 'dark' ? 'light' : 'dark' }),
    };
  }, [
    definition,
    themeId,
    ownPrefs,
    policy,
    savedToProfile,
    linkOverride,
    applySavedPrefs,
    onUserPrefsChange,
    setPolicy,
  ]);

  return <ThemeContext.Provider value={value}>{children}</ThemeContext.Provider>;
}
