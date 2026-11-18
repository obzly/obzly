import { MoreOutlined } from '@ant-design/icons';
import { Button, Dropdown } from 'antd';
import type { JSX, ReactNode } from 'react';
import { useState } from 'react';

import type { WebAnalyticsRow } from '../../../entity/webAnalytics';
import { downloadText, toCSV } from '../../../shared/lib';
import { Card } from '../../../shared/ui/v2';

export type TagTone = 'muted' | 'warn' | 'info' | 'purple';

const TAG_CLASS: Record<TagTone, string> = {
  muted: 'bg-gray-100 text-gray-500 dark:bg-gray-700 dark:text-gray-300',
  warn: 'bg-orange-500/15 text-orange-700 dark:text-orange-300',
  info: 'bg-blue-500/15 text-blue-700 dark:text-blue-300',
  purple: 'bg-purple-500/15 text-purple-700 dark:text-purple-300',
};

export const Tag = ({ text, tone }: { text: string; tone: TagTone }): JSX.Element => (
  <span
    className={`ml-1.5 inline-flex rounded-full px-1.5 text-[11px] font-medium whitespace-nowrap ${TAG_CLASS[tone]}`}
  >
    {text}
  </span>
);

export interface PanelColumn {
  label: string;
  value: (row: WebAnalyticsRow) => ReactNode;
  /** The column's value in the CSV download. */
  csv: (row: WebAnalyticsRow) => string | number;
  tone?: (row: WebAnalyticsRow) => string | undefined;
}

interface PanelFrameProps {
  title: string;
  subtitle?: ReactNode;
  /** The "Top 5 / Top N" switch; omitted for panels that show everything. */
  more?: {
    expanded: boolean;
    total: number;
    onToggle: () => void;
    expandLabel?: string;
    collapseLabel?: string;
  };
  menu: { key: string; label: string; onClick: () => void }[];
  children: ReactNode;
}

/** A panel card with its row-count switch and ⋮ menu (click, never hover). */
export const PanelFrame = ({
  title,
  subtitle,
  more,
  menu,
  children,
}: PanelFrameProps): JSX.Element => (
  <Card
    title={title}
    subtitle={subtitle}
    extra={
      <span className="flex items-center gap-1">
        {more && more.total > 5 && (
          <Button size="small" aria-pressed={more.expanded} onClick={more.onToggle}>
            {more.expanded
              ? (more.collapseLabel ?? 'Top 5')
              : (more.expandLabel ?? `Top ${more.total}`)}
          </Button>
        )}
        <Dropdown
          trigger={['click']}
          menu={{ items: menu.map((m) => ({ key: m.key, label: m.label, onClick: m.onClick })) }}
        >
          <Button size="small" icon={<MoreOutlined />} aria-label={`${title} options`} />
        </Dropdown>
      </span>
    }
  >
    {children}
  </Card>
);

interface ListPanelProps {
  title: string;
  subtitle?: ReactNode;
  keyLabel: string;
  rows: WebAnalyticsRow[];
  columns: PanelColumn[];
  color: string;
  mono?: boolean;
  label?: (row: WebAnalyticsRow) => ReactNode;
  tag?: (row: WebAnalyticsRow) => { text: string; tone: TagTone } | undefined;
  note?: ReactNode;
  empty?: ReactNode;
  onOpenRow?: (row: WebAnalyticsRow) => void;
  onOpenAll?: () => void;
  csvName: string;
}

/** A ranked list with several value columns and a bar per row. */
export const ListPanel = ({
  title,
  subtitle,
  keyLabel,
  rows,
  columns,
  color,
  mono,
  label,
  tag,
  note,
  empty,
  onOpenRow,
  onOpenAll,
  csvName,
}: ListPanelProps): JSX.Element => {
  const [expanded, setExpanded] = useState(false);
  const shown = expanded ? rows : rows.slice(0, 5);
  const max = Math.max(1, ...rows.map((r) => r.hits));

  const downloadCsv = () =>
    downloadText(
      `${csvName}.csv`,
      toCSV([
        [keyLabel, ...columns.map((c) => c.label)],
        ...rows.map((r) => [r.key, ...columns.map((c) => c.csv(r))]),
      ]),
      'text/csv',
    );
  const menu = [
    ...(onOpenAll ? [{ key: 'logs', label: 'Open in Logs', onClick: onOpenAll }] : []),
    { key: 'csv', label: 'Download CSV', onClick: downloadCsv },
  ];

  return (
    <PanelFrame
      title={title}
      subtitle={subtitle}
      more={{ expanded, total: rows.length, onToggle: () => setExpanded((v) => !v) }}
      menu={menu}
    >
      {rows.length === 0 ? (
        <div className="py-4 text-center text-sm text-gray-500 dark:text-gray-400">
          {empty ?? 'No requests in this range'}
        </div>
      ) : (
        <div
          className="grid items-center gap-x-3.5 gap-y-1.5 text-[13px]"
          style={{ gridTemplateColumns: `minmax(0, 1fr) repeat(${columns.length}, auto)` }}
        >
          <span className="text-xs text-gray-500 dark:text-gray-400">{keyLabel}</span>
          {columns.map((c) => (
            <span key={c.label} className="text-right text-xs text-gray-500 dark:text-gray-400">
              {c.label}
            </span>
          ))}
          {shown.map((row) => {
            const rowTag = tag?.(row);
            const body = (
              <>
                <span
                  className={`truncate text-gray-800 dark:text-gray-100 ${mono ? 'font-mono text-[12.5px]' : ''}`}
                  title={row.key}
                >
                  {label ? label(row) : row.key}
                  {rowTag && <Tag text={rowTag.text} tone={rowTag.tone} />}
                </span>
                <span className="block h-1 overflow-hidden rounded-full bg-[var(--v2-track)]">
                  <span
                    className="block h-full rounded-full"
                    style={{ width: `${Math.max(2, (row.hits / max) * 100)}%`, background: color }}
                  />
                </span>
              </>
            );
            return [
              onOpenRow ? (
                <button
                  key={`${row.key}-label`}
                  type="button"
                  title="Open these requests in Logs"
                  onClick={() => onOpenRow(row)}
                  className="flex min-w-0 cursor-pointer flex-col gap-1 border-0 bg-transparent p-0 text-left"
                >
                  {body}
                </button>
              ) : (
                <span key={`${row.key}-label`} className="flex min-w-0 flex-col gap-1">
                  {body}
                </span>
              ),
              ...columns.map((c) => (
                <span
                  key={`${row.key}-${c.label}`}
                  className={`text-right whitespace-nowrap ${c.tone?.(row) ?? 'text-gray-900 dark:text-gray-50'}`}
                >
                  {c.value(row)}
                </span>
              )),
            ];
          })}
        </div>
      )}
      {note && <p className="m-0 mt-2.5 text-xs text-gray-500 dark:text-gray-400">{note}</p>}
    </PanelFrame>
  );
};
