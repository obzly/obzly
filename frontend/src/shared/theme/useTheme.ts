import { useContext } from 'react';

import { ThemeContext } from './themeContextValue';
import type { ThemeContextValue } from './themeContextValue';

export function useTheme(): ThemeContextValue {
  const context = useContext(ThemeContext);
  if (!context) {
    throw new Error('useTheme must be used within a ThemeProvider');
  }
  return context;
}
