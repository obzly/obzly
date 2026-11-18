import type { JSX } from 'react';

interface Props {
  values: number[];
  color: string;
  height?: number;
  label?: string;
}

/** Small area+line trend. Draws nothing for fewer than two points. */
export const Sparkline = ({ values, color, height = 36, label }: Props): JSX.Element | null => {
  if (values.length < 2) return null;
  const w = 240;
  const h = height;
  const max = Math.max(...values);
  const min = Math.min(...values);
  const span = max - min || 1;
  const pts = values.map((v, i) => [
    (i / (values.length - 1)) * w,
    3 + (1 - (v - min) / span) * (h - 6),
  ]);
  const line = pts.map((p, i) => `${i ? 'L' : 'M'}${p[0].toFixed(1)} ${p[1].toFixed(1)}`).join(' ');
  return (
    <svg
      viewBox={`0 0 ${w} ${h}`}
      preserveAspectRatio="none"
      className="block w-full"
      style={{ height: h }}
      role={label ? 'img' : undefined}
      aria-label={label}
      aria-hidden={label ? undefined : true}
    >
      <path d={`${line} L${w} ${h} L0 ${h} Z`} fill={color} opacity={0.14} />
      <path d={line} fill="none" stroke={color} strokeWidth={2} strokeLinejoin="round" />
    </svg>
  );
};
