import { Tooltip } from 'antd';
import React from 'react';

export interface ToggleOption<T> {
  value: T;
  label: React.ReactNode;
  pressed: boolean;
  /** Tooltip text. */
  hint?: string;
}

interface Props<T> {
  label: string;
  options: ToggleOption<T>[];
  onToggle: (value: T) => void;
  size?: 'small' | 'default';
}

/** A v2 segmented group of pressable pills (one or several pressed). */
export const ToggleGroupComponent = <T extends string | number>({
  label,
  options,
  onToggle,
  size = 'default',
}: Props<T>): React.JSX.Element => {
  const pill =
    size === 'small'
      ? 'min-h-6 rounded-md px-2 py-0.5 text-xs'
      : 'min-h-7 rounded-lg px-3 text-[13px]';
  return (
    <div
      role="group"
      aria-label={label}
      className={`inline-flex flex-wrap gap-px border border-gray-200 bg-gray-50 dark:border-gray-700 dark:bg-gray-900/60 ${size === 'small' ? 'rounded-lg p-0.5' : 'rounded-[10px] p-[3px]'}`}
    >
      {options.map((option) => {
        const button = (
          <button
            key={String(option.value)}
            type="button"
            aria-pressed={option.pressed}
            onClick={() => onToggle(option.value)}
            className={`${pill} focus-visible:ring-accent-500 cursor-pointer border-0 transition-colors focus-visible:ring-2 focus-visible:outline-none ${
              option.pressed
                ? 'bg-accent-600 dark:bg-accent-500 font-medium text-white dark:text-gray-950'
                : 'bg-transparent text-gray-500 hover:text-gray-900 dark:text-gray-300 dark:hover:text-gray-50'
            }`}
          >
            {option.label}
          </button>
        );
        return option.hint ? (
          <Tooltip key={String(option.value)} title={option.hint}>
            {button}
          </Tooltip>
        ) : (
          button
        );
      })}
    </div>
  );
};
