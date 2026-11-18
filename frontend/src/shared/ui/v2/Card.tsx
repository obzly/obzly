import type { JSX, ReactNode } from 'react';

interface Props {
  title?: ReactNode;
  subtitle?: ReactNode;
  extra?: ReactNode;
  className?: string;
  bodyClassName?: string;
  children?: ReactNode;
}

/** The v2 widget card: rounded panel with an optional title row. */
export const Card = ({
  title,
  subtitle,
  extra,
  className = '',
  bodyClassName = '',
  children,
}: Props): JSX.Element => (
  <section
    className={`flex min-w-0 flex-col gap-3 rounded-2xl border border-gray-200 bg-white p-5 dark:border-gray-700 dark:bg-gray-800 ${className}`}
  >
    {(title || extra) && (
      <div className="flex flex-wrap items-start justify-between gap-2">
        <div className="min-w-0">
          {title && (
            <h2 className="m-0 text-[15px] font-semibold text-gray-900 dark:text-gray-50">
              {title}
            </h2>
          )}
          {subtitle && (
            <p className="m-0 mt-0.5 text-xs text-gray-500 dark:text-gray-400">{subtitle}</p>
          )}
        </div>
        {extra && <div className="flex flex-wrap items-center gap-2">{extra}</div>}
      </div>
    )}
    <div className={`min-w-0 ${bodyClassName}`}>{children}</div>
  </section>
);
