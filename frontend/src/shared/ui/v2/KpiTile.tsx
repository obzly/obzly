import type { JSX, ReactNode } from 'react';

import { ChangePill, type ChangeTone } from './ChangePill';
import { Sparkline } from './Sparkline';

interface Props {
  label: string;
  value: ReactNode;
  unit?: string;
  change?: { text: string; tone: ChangeTone };
  note?: ReactNode;
  trend?: number[];
  color?: string;
  icon?: ReactNode;
  onClick?: () => void;
}

/** Headline number with its change and an optional sparkline. Unknown values show as given (e.g. "n/a"), never 0. */
export const KpiTile = ({
  label,
  value,
  unit,
  change,
  note,
  trend,
  color = 'var(--v2-accent)',
  icon,
  onClick,
}: Props): JSX.Element => {
  const body = (
    <>
      <span className="flex items-start justify-between gap-2 text-sm text-gray-500 dark:text-gray-400">
        <span>{label}</span>
        {icon ?? (change && <ChangePill text={change.text} tone={change.tone} />)}
      </span>
      <span className="text-[28px] leading-none font-semibold text-gray-900 dark:text-gray-50">
        {value}
        {unit && (
          <small className="ml-1 text-sm font-medium text-gray-500 dark:text-gray-400">
            {unit}
          </small>
        )}
      </span>
      {icon && change && (
        <span>
          <ChangePill text={change.text} tone={change.tone} />
        </span>
      )}
      {note && <span className="text-xs text-gray-500 dark:text-gray-400">{note}</span>}
      {trend && <Sparkline values={trend} color={color} label={`${label} trend`} />}
    </>
  );
  const cls =
    'flex min-w-0 flex-col gap-2 rounded-2xl border border-gray-200 bg-white p-4 text-left dark:border-gray-700 dark:bg-gray-800';
  return onClick ? (
    <button
      type="button"
      onClick={onClick}
      className={`${cls} hover:border-accent-500/60 cursor-pointer`}
    >
      {body}
    </button>
  ) : (
    <div className={cls}>{body}</div>
  );
};
