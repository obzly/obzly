import type { HTMLAttributes, JSX, ReactNode } from 'react';

export type PillTone = 'bad' | 'warn' | 'ok' | 'info' | 'purple' | 'muted';

const TONE: Record<PillTone, string> = {
  bad: 'bg-red-500/15 text-[var(--v2-red)]',
  warn: 'bg-amber-500/15 text-[var(--v2-orange)]',
  // Green in every theme: OK is a status, not the brand accent.
  ok: 'bg-emerald-600/10 text-emerald-600 dark:bg-emerald-500/15 dark:text-emerald-500',
  info: 'bg-blue-500/15 text-[var(--v2-blue)]',
  purple: 'bg-violet-500/15 text-[var(--v2-purple)]',
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

/** Small bordered chip for label pairs and channel names. */
export const Chip = ({
  children,
  mono,
  danger,
}: {
  children: ReactNode;
  mono?: boolean;
  danger?: boolean;
}): JSX.Element => (
  <span
    className={`inline-flex items-center rounded-md border bg-[var(--v2-panel2)] px-1.5 py-px text-[11.5px] ${
      mono ? 'font-mono' : ''
    } ${
      danger
        ? 'border-dashed border-red-400 text-[var(--v2-red)]'
        : 'border-[var(--v2-border)] text-gray-800 dark:text-gray-100'
    }`}
  >
    {children}
  </span>
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
export const Field = ({
  label,
  help,
  children,
  className = '',
}: {
  label: string;
  help?: string;
  children: ReactNode;
  className?: string;
}): JSX.Element => (
  <div className={`flex min-w-0 flex-col gap-1 ${className}`}>
    <span className="text-[13px] font-medium text-gray-700 dark:text-gray-200">{label}</span>
    {children}
    {help && <span className="text-xs text-gray-500 dark:text-gray-400">{help}</span>}
  </div>
);
