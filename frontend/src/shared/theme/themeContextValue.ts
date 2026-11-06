import { createContext } from 'react';

import type { ThemeMode } from './themeStorage';

export interface ThemeContextValue {
  theme: ThemeMode;
  toggleTheme: () => void;
}

export const ThemeContext = createContext<ThemeContextValue | null>(null);
