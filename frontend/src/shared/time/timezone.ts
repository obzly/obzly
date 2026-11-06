import dayjs from 'dayjs';

const STORAGE_KEY = 'obzly-display-timezone';
/** Fired on window when the display timezone changes, so every view follows it. */
export const TIMEZONE_CHANGE_EVENT = 'obzly-display-timezone-change';

/** Sentinel for the browser's timezone; compared against, never passed to dayjs.tz(). */
export const LOCAL_TIMEZONE = 'Local';
export const UTC_TIMEZONE = 'UTC';

/** A curated, always-available fallback list, ordered roughly UTC-west-to-east. */
const FALLBACK_TIMEZONES = [
  'Pacific/Honolulu',
  'America/Anchorage',
  'America/Los_Angeles',
  'America/Denver',
  'America/Chicago',
  'America/New_York',
  'America/Sao_Paulo',
  UTC_TIMEZONE,
  'Europe/London',
  'Europe/Paris',
  'Europe/Berlin',
  'Europe/Moscow',
  'Asia/Dubai',
  'Asia/Kolkata',
  'Asia/Bangkok',
  'Asia/Shanghai',
  'Asia/Tokyo',
  'Australia/Sydney',
  'Pacific/Auckland',
];

/** The browser's IANA timezones (no bundled tz database); a curated list on older browsers. */
export const getAvailableTimezones = (): string[] => {
  try {
    const supportedValuesOf = (Intl as unknown as { supportedValuesOf?: (key: string) => string[] })
      .supportedValuesOf;
    if (supportedValuesOf) {
      return supportedValuesOf('timeZone');
    }
  } catch {
    // fall through to the static list below
  }
  return FALLBACK_TIMEZONES;
};

export const getBrowserTimezone = (): string => {
  try {
    return Intl.DateTimeFormat().resolvedOptions().timeZone;
  } catch {
    return UTC_TIMEZONE;
  }
};

export const getStoredTimezone = (): string => {
  if (typeof window === 'undefined') {
    return LOCAL_TIMEZONE;
  }

  try {
    return localStorage.getItem(STORAGE_KEY) || LOCAL_TIMEZONE;
  } catch {
    return LOCAL_TIMEZONE;
  }
};

export const setStoredTimezone = (timezone: string): void => {
  if (typeof window === 'undefined') {
    return;
  }

  try {
    localStorage.setItem(STORAGE_KEY, timezone);
  } catch (error) {
    console.warn('Failed to save display timezone to localStorage:', error);
  }
  window.dispatchEvent(new CustomEvent(TIMEZONE_CHANGE_EVENT, { detail: timezone }));
};

/**
 * Milliseconds to add to an instant so that, rendered as UTC, it shows the
 * wall-clock time in `timezone` (charts can only render local time or UTC).
 */
export const timezoneShiftMs = (ms: number, timezone: string): number => {
  if (timezone === UTC_TIMEZONE) {
    return 0;
  }
  if (!timezone || timezone === LOCAL_TIMEZONE) {
    return -new Date(ms).getTimezoneOffset() * 60_000;
  }
  return dayjs(ms).tz(timezone).utcOffset() * 60_000;
};

/**
 * The instant a shifted (timezoneShiftMs) value stands for. In the hour a
 * clock change repeats, the wall time is ambiguous and the later instant wins.
 */
export const unshiftTimezone = (shifted: number, timezone: string): number =>
  shifted - timezoneShiftMs(shifted - timezoneShiftMs(shifted, timezone), timezone);

export const formatInTimezone = (
  date: string | number | Date,
  format: string,
  timezone: string,
): string => {
  if (!timezone || timezone === LOCAL_TIMEZONE) {
    return dayjs(date).format(format);
  }
  return dayjs(date).tz(timezone).format(format);
};

/** Short, human label for a timezone Select option, e.g. "UTC" or "America/New_York (UTC-05:00)". */
export const getTimezoneLabel = (timezone: string): string => {
  if (timezone === LOCAL_TIMEZONE) {
    return `Local (${getBrowserTimezone()})`;
  }
  if (timezone === UTC_TIMEZONE) {
    return UTC_TIMEZONE;
  }

  const offset = dayjs().tz(timezone).format('Z');
  return `${timezone} (UTC${offset})`;
};
