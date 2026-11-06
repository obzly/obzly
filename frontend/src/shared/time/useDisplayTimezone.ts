import { useCallback, useEffect, useState } from 'react';

import { TIMEZONE_CHANGE_EVENT, getStoredTimezone, setStoredTimezone } from './timezone';

/** The display timezone, shared by every view and kept across reloads. */
export const useDisplayTimezone = (): [string, (timezone: string) => void] => {
  const [timezone, setTimezone] = useState<string>(getStoredTimezone);

  useEffect(() => {
    const sync = () => setTimezone(getStoredTimezone());
    window.addEventListener(TIMEZONE_CHANGE_EVENT, sync);
    window.addEventListener('storage', sync);
    return () => {
      window.removeEventListener(TIMEZONE_CHANGE_EVENT, sync);
      window.removeEventListener('storage', sync);
    };
  }, []);

  const update = useCallback((value: string) => {
    setStoredTimezone(value);
    setTimezone(value);
  }, []);

  return [timezone, update];
};
