import { useEffect, useState } from 'react';
import type { JSX, ReactNode } from 'react';

import { ThemeContext } from './themeContextValue';
import { type ThemeMode, getInitialTheme, setStoredTheme } from './themeStorage';

export function ThemeProvider({ children }: { children: ReactNode }): JSX.Element {
  const [theme, setTheme] = useState<ThemeMode>(getInitialTheme);

  useEffect(() => {
    // Tailwind's `dark:` keys off this class, so the toggle overrides the OS setting.
    document.documentElement.classList.toggle('dark', theme === 'dark');
    setStoredTheme(theme);
  }, [theme]);

  const toggleTheme = () => {
    setTheme((prev) => (prev === 'light' ? 'dark' : 'light'));
  };

  return <ThemeContext.Provider value={{ theme, toggleTheme }}>{children}</ThemeContext.Provider>;
}
