import type { JSX, ReactNode } from 'react';

interface Props {
  title: ReactNode;
  subtitle?: ReactNode;
  actions?: ReactNode;
}

/** Page title row used at the top of every v2 page. */
export const PageHeader = ({ title, subtitle, actions }: Props): JSX.Element => (
  <div className="flex flex-wrap items-end gap-3">
    <div className="min-w-[240px] flex-1">
      <h1 className="m-0 text-2xl font-semibold text-gray-900 dark:text-gray-50">{title}</h1>
      {subtitle && <p className="m-0 mt-1 text-sm text-gray-500 dark:text-gray-400">{subtitle}</p>}
    </div>
    {actions && <div className="flex flex-wrap items-center gap-2">{actions}</div>}
  </div>
);
