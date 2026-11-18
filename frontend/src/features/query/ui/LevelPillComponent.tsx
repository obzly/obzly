import React from 'react';

import { levelColor } from '../lib/logLevels';

/** A log level as a soft pill in the level's own color (readable in both themes). */
export const LevelPillComponent = ({
  level,
  className = '',
}: {
  level: string;
  className?: string;
}): React.JSX.Element => {
  const color = levelColor(level || 'unknown');
  return (
    <span
      className={`inline-flex items-center rounded-full px-1.5 text-[11px] leading-[18px] font-medium whitespace-nowrap ${className}`}
      style={{ color, background: `color-mix(in srgb, ${color} 15%, transparent)` }}
    >
      {level || 'unknown'}
    </span>
  );
};
