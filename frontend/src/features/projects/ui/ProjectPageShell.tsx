import type { JSX, ReactNode } from 'react';

import { PageHeader } from '../../../shared/ui/v2';

interface ShellProps {
  /** Inside ProjectSettingsGroupComponent, which draws the page frame and header itself. */
  embedded?: boolean;
  contentHeight: number;
  title: ReactNode;
  subtitle?: ReactNode;
  children: ReactNode;
}

/** Page frame of a project settings section, or just its body when embedded in the group. */
export const ProjectPageShell = ({
  embedded,
  contentHeight,
  title,
  subtitle,
  children,
}: ShellProps): JSX.Element => {
  const body = <div className="flex min-w-0 flex-col gap-4">{children}</div>;
  if (embedded) return body;
  return (
    <div className="flex grow">
      <div className="w-full">
        <div
          className="grow overflow-y-auto rounded-2xl border border-gray-200 bg-white p-5 dark:border-gray-700 dark:bg-gray-800"
          style={{ height: contentHeight }}
        >
          <div className="flex flex-col gap-5">
            <PageHeader title={title} subtitle={subtitle} />
            {body}
          </div>
        </div>
      </div>
    </div>
  );
};

export const BANNER_WARN =
  'rounded-xl bg-orange-500/10 px-3.5 py-3 text-[13.5px] text-[var(--v2-orange)]';
export const BANNER_NOTE =
  'rounded-xl bg-gray-50 px-3.5 py-3 text-[13.5px] text-gray-600 dark:bg-gray-900/40 dark:text-gray-300';

export type PillTone = 'ok' | 'warn' | 'bad' | 'neutral' | 'info';

const PILL_TONE: Record<PillTone, string> = {
  ok: 'bg-[var(--v2-ok-soft)] text-[var(--v2-accent)]',
  warn: 'bg-orange-500/15 text-[var(--v2-orange)]',
  bad: 'bg-red-500/15 text-[var(--v2-red)]',
  neutral: 'bg-gray-100 text-gray-600 dark:bg-gray-700 dark:text-gray-300',
  info: 'bg-blue-500/15 text-[var(--v2-blue)]',
};

export const Pill = ({ tone, children }: { tone: PillTone; children: ReactNode }): JSX.Element => (
  <span
    className={`inline-flex items-center gap-1 rounded-full px-2 py-0.5 text-xs font-medium whitespace-nowrap ${PILL_TONE[tone]}`}
  >
    {children}
  </span>
);

/** "Unsaved changes" footer of a card; the buttons are the caller's. */
export const SaveBar = ({ children }: { children: ReactNode }): JSX.Element => (
  <div className="mt-1 flex flex-wrap items-center gap-2 border-t border-gray-200 pt-3 dark:border-gray-700">
    <span className="flex-1 text-xs text-gray-500 dark:text-gray-400">Unsaved changes</span>
    {children}
  </div>
);

const initialsOf = (name: string, email: string): string => {
  const source = name.trim() || email;
  const parts = source.split(/[\s@._-]+/).filter(Boolean);
  return ((parts[0]?.[0] ?? '') + (parts[1]?.[0] ?? '')).toUpperCase() || '?';
};

export const Avatar = ({ name, email }: { name: string; email: string }): JSX.Element => (
  <span
    aria-hidden="true"
    className="flex h-8 w-8 shrink-0 items-center justify-center rounded-full bg-[var(--v2-ok-soft)] text-xs font-semibold text-[var(--v2-accent)]"
  >
    {initialsOf(name, email)}
  </span>
);
