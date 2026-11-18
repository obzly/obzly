import type { JSX, ReactNode } from 'react';

export interface DonutPart {
  key: string;
  value: number;
  color: string;
}

interface Props {
  parts: DonutPart[];
  size?: number;
  center?: ReactNode;
  centerSub?: ReactNode;
  label: string;
}

/** Ring chart with a total in the middle. Always pair it with a legend that shows numbers. */
export const Donut = ({ parts, size = 140, center, centerSub, label }: Props): JSX.Element => {
  const r = 40;
  const c = 2 * Math.PI * r;
  const total = parts.reduce((a, p) => a + p.value, 0) || 1;
  let acc = 0;
  return (
    <span className="relative inline-block shrink-0" style={{ width: size, height: size }}>
      <svg
        viewBox="0 0 100 100"
        width={size}
        height={size}
        role="img"
        aria-label={label}
        style={{ transform: 'rotate(-90deg)' }}
      >
        <circle cx="50" cy="50" r={r} fill="none" stroke="var(--v2-track)" strokeWidth="12" />
        {parts.map((p) => {
          const len = (c * p.value) / total;
          const el = (
            <circle
              key={p.key}
              cx="50"
              cy="50"
              r={r}
              fill="none"
              stroke={p.color}
              strokeWidth="12"
              strokeDasharray={`${Math.max(len - 1.5, 0.6)} ${c}`}
              strokeDashoffset={-acc}
            />
          );
          acc += len;
          return el;
        })}
      </svg>
      <span className="absolute inset-0 flex flex-col items-center justify-center">
        {center !== undefined && (
          <strong className="text-xl font-semibold text-gray-900 dark:text-gray-50">
            {center}
          </strong>
        )}
        {centerSub && <span className="text-xs text-gray-500 dark:text-gray-400">{centerSub}</span>}
      </span>
    </span>
  );
};
