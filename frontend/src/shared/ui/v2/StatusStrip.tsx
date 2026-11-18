import type { JSX } from 'react';

export type StripState = 'ok' | 'warn' | 'fail' | 'none';

interface Props {
  cells: { state: StripState; title?: string }[];
  height?: number;
  label?: string;
}

// Status colours, the same in every theme: ok is green even when the accent is not.
const COLOR: Record<StripState, string> = {
  ok: 'bg-[var(--v2-green,#10b981)]',
  warn: 'bg-[var(--v2-orange)]',
  fail: 'bg-[var(--v2-red)]',
  none: 'bg-[var(--v2-track)]', // no data is grey, never "ok"
};

/** One cell per time bucket, coloured by state. */
export const StatusStrip = ({ cells, height = 14, label }: Props): JSX.Element => (
  <span className="flex gap-0.5" role={label ? 'img' : undefined} aria-label={label}>
    {cells.map((c, i) => (
      <span
        key={i}
        title={c.title}
        className={`flex-1 rounded-sm ${COLOR[c.state]}`}
        style={{ height }}
      />
    ))}
  </span>
);
