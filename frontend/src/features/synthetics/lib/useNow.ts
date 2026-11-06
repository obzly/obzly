import { useEffect, useState } from 'react';

/** The current time, updated every `everyMs`, so "in 18 min" and "active" stay right. */
export const useNow = (everyMs = 30_000): number => {
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    const timer = setInterval(() => setNow(Date.now()), everyMs);
    return () => clearInterval(timer);
  }, [everyMs]);
  return now;
};
