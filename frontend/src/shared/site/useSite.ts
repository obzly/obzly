import { useContext } from 'react';

import { SiteContext } from './siteContextValue';
import type { SiteContextValue } from './siteContextValue';

export function useSite(): SiteContextValue {
  const context = useContext(SiteContext);
  if (!context) {
    throw new Error('useSite must be used within a SiteProvider');
  }
  return context;
}
