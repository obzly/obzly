/** 12- or 24-hour short format, following the browser locale. */
export const getUserShortTimeFormat = (): { use12Hours: boolean; format: string } => {
  const locale = navigator.language || 'en-US';
  const testDate = new Date(2023, 0, 1, 13, 0, 0); // 1 PM
  const timeString = testDate.toLocaleTimeString(locale, { hour: 'numeric' });
  const is12Hour = timeString.includes('PM') || timeString.includes('AM');

  return {
    use12Hours: is12Hour,
    format: is12Hour ? 'DD MMM YYYY h:mm A' : 'DD MMM YYYY HH:mm',
  };
};

/** Like getUserShortTimeFormat, with milliseconds. */
export const getUserTimeFormatWithMs = (): { use12Hours: boolean; format: string } => {
  const locale = navigator.language || 'en-US';
  const testDate = new Date(2023, 0, 1, 13, 0, 0); // 1 PM
  const timeString = testDate.toLocaleTimeString(locale, { hour: 'numeric' });
  const is12Hour = timeString.includes('PM') || timeString.includes('AM');

  return {
    use12Hours: is12Hour,
    format: is12Hour ? 'MMM D h:mm:ss.SSS A' : 'MMM D HH:mm:ss.SSS',
  };
};
