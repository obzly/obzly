import type { JSX } from 'react';

export type ChangeTone = 'good' | 'bad' | 'neutral';

interface Props {
  text: string;
  /** Coloured by whether the change is good or bad, never by its sign. */
  tone: ChangeTone;
}

const TONE: Record<ChangeTone, string> = {
  // Good is the status green in every theme, not the accent.
  good: 'bg-[var(--v2-green-soft,rgba(16,185,129,0.14))] text-[var(--v2-green,#10b981)]',
  bad: 'bg-red-500/15 text-[var(--v2-red)]',
  neutral: 'bg-gray-100 text-gray-500 dark:bg-gray-700 dark:text-gray-300',
};

export const ChangePill = ({ text, tone }: Props): JSX.Element => (
  <span
    className={`inline-flex items-center rounded-full px-2 py-0.5 text-xs font-medium whitespace-nowrap ${TONE[tone]}`}
  >
    {text}
  </span>
);
