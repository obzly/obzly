import type { JSX, ReactNode } from 'react';

interface Props {
  /** 0..1, or null when unknown (the arc stays empty and the value says so). */
  ratio: number | null;
  value: ReactNode;
  caption?: ReactNode;
  color?: string;
  size?: number;
  label: string;
}

/** 270° gauge for a value against a known maximum (budgets, quotas, disk, SLO). */
export const Gauge = ({
  ratio,
  value,
  caption,
  color = 'var(--v2-accent)',
  size = 140,
  label,
}: Props): JSX.Element => {
  const arc = 197.9; // 270° of a r=42 circle
  return (
    <span className="relative inline-block shrink-0" style={{ width: size, height: size }}>
      <svg
        viewBox="0 0 100 100"
        width={size}
        height={size}
        role="img"
        aria-label={label}
        style={{ transform: 'rotate(135deg)' }}
      >
        <circle
          cx="50"
          cy="50"
          r="42"
          fill="none"
          stroke="var(--v2-track)"
          strokeWidth="9"
          strokeLinecap="round"
          strokeDasharray={`${arc} 264`}
        />
        {ratio !== null && (
          <circle
            cx="50"
            cy="50"
            r="42"
            fill="none"
            stroke={color}
            strokeWidth="9"
            strokeLinecap="round"
            strokeDasharray={`${(arc * Math.min(Math.max(ratio, 0), 1)).toFixed(1)} 264`}
          />
        )}
      </svg>
      <span className="absolute inset-0 flex flex-col items-center justify-center">
        <span className="text-2xl font-semibold text-gray-900 dark:text-gray-50">{value}</span>
        {caption && <span className="text-xs text-gray-500 dark:text-gray-400">{caption}</span>}
      </span>
    </span>
  );
};
