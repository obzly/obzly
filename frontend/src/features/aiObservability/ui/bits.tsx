import { InfoCircleOutlined } from '@ant-design/icons';
import { Tooltip } from 'antd';
import type { HTMLAttributes, JSX, ReactNode } from 'react';

import type { PillTone } from '../lib/describeEvent';

const TONE: Record<PillTone, string> = {
  bad: 'bg-red-500/15 text-[var(--v2-red)]',
  warn: 'bg-amber-500/15 text-[var(--v2-orange)]',
  // Green in every theme: OK is a status, not the brand accent.
  ok: 'bg-emerald-600/10 text-emerald-600 dark:bg-emerald-500/15 dark:text-emerald-500',
  info: 'bg-blue-500/15 text-[var(--v2-blue)]',
  muted: 'bg-[var(--v2-panel2)] text-gray-700 dark:text-gray-200',
};

/** Rounded status pill; props are spread so antd Tooltip can wrap it. */
export const Pill = ({
  tone,
  children,
  className = '',
  ...rest
}: HTMLAttributes<HTMLSpanElement> & { tone: PillTone; children: ReactNode }): JSX.Element => (
  <span
    {...rest}
    className={`inline-flex items-center gap-1.5 rounded-full px-2.5 py-0.5 text-xs font-medium whitespace-nowrap ${TONE[tone]} ${className}`}
  >
    {children}
  </span>
);

/** Mono chip for model names and ids. */
export const Chip = ({ children }: { children: ReactNode }): JSX.Element => (
  <span className="inline-flex items-center rounded-md border border-[var(--v2-border)] bg-[var(--v2-panel2)] px-1.5 py-px font-mono text-[11px] text-gray-800 dark:text-gray-100">
    {children}
  </span>
);

/** An "i" that explains a number; opens on hover, focus or click so it works without a mouse. */
export const InfoTip = ({ label, text }: { label: string; text: string }): JSX.Element => (
  <Tooltip title={text} trigger={['hover', 'focus', 'click']}>
    <button
      type="button"
      aria-label={`About ${label}`}
      className="inline-flex cursor-pointer border-0 bg-transparent p-0 text-gray-500 hover:text-gray-800 dark:text-gray-400 dark:hover:text-gray-100"
    >
      <InfoCircleOutlined />
    </button>
  </Tooltip>
);

interface TabItem<K extends string> {
  key: K;
  label: string;
}

/** The v2 page tabs: soft accent pill on the selected one. */
export const PillTabs = <K extends string>({
  items,
  active,
  onChange,
  label,
}: {
  items: TabItem<K>[];
  active: K;
  onChange: (key: K) => void;
  label: string;
}): JSX.Element => (
  <div role="tablist" aria-label={label} className="flex flex-wrap gap-1">
    {items.map((item) => {
      const on = item.key === active;
      return (
        <button
          key={item.key}
          type="button"
          role="tab"
          aria-selected={on}
          onClick={() => onChange(item.key)}
          className={`min-h-9 cursor-pointer rounded-[10px] border-0 px-3.5 py-2 text-sm ${
            on
              ? 'bg-[var(--v2-ok-soft)] font-semibold text-[var(--v2-accent)]'
              : 'bg-transparent text-gray-500 hover:text-gray-900 dark:text-gray-400 dark:hover:text-gray-50'
          }`}
        >
          {item.label}
        </button>
      );
    })}
  </div>
);

/** Label above a form control, with optional help underneath. */
export const FieldLabel = ({ text, help }: { text: string; help?: string }): JSX.Element => (
  <div className="mb-1">
    <span className="text-[13px] font-medium text-gray-700 dark:text-gray-200">{text}</span>
    {help && <div className="text-xs text-gray-500 dark:text-gray-400">{help}</div>}
  </div>
);

/** The small footer stats under a chart. */
export const FootStats = ({
  items,
}: {
  items: { label: string; value: ReactNode }[];
}): JSX.Element => (
  <div className="grid grid-cols-[repeat(auto-fit,minmax(140px,1fr))] gap-2.5 border-t border-gray-200 pt-3 dark:border-gray-700">
    {items.map((item) => (
      <span key={item.label} className="flex flex-col">
        <span className="text-xs text-gray-500 dark:text-gray-400">{item.label}</span>
        <b className="text-[15px] font-semibold text-gray-900 dark:text-gray-50">{item.value}</b>
      </span>
    ))}
  </div>
);
