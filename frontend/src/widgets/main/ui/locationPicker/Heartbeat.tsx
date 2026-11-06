import type { JSX } from 'react';

import type { SiteHealth } from '../../../../entity/sites';
import { HEALTH_LABEL } from './healthLabels';

const BEAT = 'M0 7h8l2-5 3 10 3-8 2 3h12';
const WEAK_BEAT = 'M0 7h8l2-3 3 6 3-4 2 1h12';
const FLAT = 'M0 7h30';

// Shape carries the status as well as colour, so it reads without colour.
const STYLE: Record<SiteHealth, { d: string; color: string; className?: string; dash?: string }> = {
  up: { d: BEAT, color: '#10b981', className: 'obzly-heartbeat' },
  degraded: { d: WEAK_BEAT, color: '#f59e0b', className: 'obzly-heartbeat obzly-heartbeat-slow' },
  down: { d: FLAT, color: '#ef4444' },
  disabled: { d: FLAT, color: '#9ca3af', dash: '3 3' },
};

export const Heartbeat = ({ status }: { status?: SiteHealth }): JSX.Element => {
  const style = status
    ? STYLE[status]
    : { d: FLAT, color: '#d1d5db', dash: '1 3', className: undefined };
  const label = status ? HEALTH_LABEL[status] : 'Checking status';
  return (
    <svg
      width="30"
      height="14"
      viewBox="0 0 30 14"
      role="img"
      aria-label={`Site status: ${label}`}
      className="shrink-0"
    >
      <path
        d={style.d}
        fill="none"
        stroke={style.color}
        strokeWidth={1.6}
        strokeLinecap="round"
        strokeLinejoin="round"
        strokeDasharray={style.dash}
        className={style.className}
      />
    </svg>
  );
};
