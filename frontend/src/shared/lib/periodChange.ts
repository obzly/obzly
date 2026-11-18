// Change pills against an earlier value (the previous period, 24 h ago). Coloured by whether
// the change is good or bad, never by its sign; nothing to compare with means no pill.
import type { ChangeTone } from '../ui/v2/ChangePill';

/** Which way is good news: up, down, or neither (a plain count). */
export type Better = 'up' | 'down' | 'neither';

export interface Change {
  text: string;
  tone: ChangeTone;
}

const toneOf = (delta: number, better: Better): ChangeTone => {
  if (delta === 0 || better === 'neither') return 'neutral';
  return delta > 0 === (better === 'up') ? 'good' : 'bad';
};

/** "+12%" against before; undefined when either is unknown or before is 0. */
export const relativeChange = (
  now: number | null | undefined,
  before: number | null | undefined,
  better: Better,
): Change | undefined => {
  if (now === null || now === undefined || before === null || before === undefined)
    return undefined;
  if (before === 0) return now === 0 ? { text: '0%', tone: 'neutral' } : undefined;
  const percent = ((now - before) / Math.abs(before)) * 100;
  const shown = Math.abs(percent) < 10 ? percent.toFixed(1) : percent.toFixed(0);
  const delta = Number(shown);
  return {
    text: `${delta > 0 ? '+' : ''}${delta === 0 ? '0' : shown}%`,
    tone: toneOf(delta, better),
  };
};

/** "+3", "-0.4 s", "+0.2 pts": the difference, formatted; undefined when either is unknown. */
export const absoluteChange = (
  now: number | null | undefined,
  before: number | null | undefined,
  better: Better,
  format: (difference: number) => string = (difference) => difference.toLocaleString(),
): Change | undefined => {
  if (now === null || now === undefined || before === null || before === undefined)
    return undefined;
  const delta = now - before;
  const text = format(Math.abs(delta));
  return {
    text: delta === 0 ? `±${text}` : `${delta > 0 ? '+' : '-'}${text}`,
    tone: toneOf(delta, better),
  };
};
