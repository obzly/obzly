export const errorMessage = (error: unknown, fallback: string) =>
  error instanceof Error ? error.message : fallback;

export type PillTone = 'ok' | 'warn' | 'bad' | 'neutral' | 'info';

const PILL_TONE: Record<PillTone, string> = {
  ok: 'bg-[var(--v2-ok-soft)] text-[var(--v2-accent)]',
  warn: 'bg-orange-500/15 text-[var(--v2-orange)]',
  bad: 'bg-red-500/15 text-[var(--v2-red)]',
  neutral: 'bg-gray-100 text-gray-600 dark:bg-gray-700 dark:text-gray-300',
  info: 'bg-blue-500/15 text-[var(--v2-blue)]',
};

export const pillClass = (tone: PillTone) =>
  `inline-flex items-center gap-1 rounded-full px-2 py-0.5 text-xs font-medium ${PILL_TONE[tone]}`;

export const NOTE_CLASS =
  'rounded-xl bg-gray-50 px-3.5 py-3 text-[13.5px] text-gray-600 dark:bg-gray-900/40 dark:text-gray-300';

export const WARN_CLASS =
  'rounded-xl bg-orange-500/10 px-3.5 py-3 text-[13.5px] text-[var(--v2-orange)]';

export const AVATAR_CLASS =
  'flex h-8 w-8 shrink-0 items-center justify-center rounded-full bg-[var(--v2-ok-soft)] text-xs font-semibold text-[var(--v2-accent)]';

/** Up to two initials from a name, falling back to the email. */
export const initialsOf = (name: string, email = '') => {
  const words = (name || email)
    .trim()
    .split(/[\s@._-]+/)
    .filter(Boolean);
  const letters = words.length > 1 ? words[0][0] + words[1][0] : (words[0] ?? '?').slice(0, 2);
  return letters.toUpperCase();
};

/** Binary units, like Obzly settings → Overview. */
export const formatBytes = (bytes: number): string => {
  if (bytes <= 0) return '0 B';
  const units = ['B', 'KB', 'MB', 'GB', 'TB', 'PB'];
  const exponent = Math.min(Math.floor(Math.log(bytes) / Math.log(1024)), units.length - 1);
  const value = bytes / 1024 ** exponent;
  return `${value.toFixed(value >= 10 || exponent === 0 ? 0 : 1)} ${units[exponent]}`;
};
