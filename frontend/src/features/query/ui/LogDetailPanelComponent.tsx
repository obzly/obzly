import {
  ApartmentOutlined,
  CloseOutlined,
  ColumnHeightOutlined,
  CopyOutlined,
  MinusCircleOutlined,
  PlusCircleOutlined,
} from '@ant-design/icons';
import { Button, Segmented, Tooltip } from 'antd';
import React, { useState } from 'react';
import { Prism as SyntaxHighlighter } from 'react-syntax-highlighter';
import { tomorrow } from 'react-syntax-highlighter/dist/esm/styles/prism';

import type { LogItem } from '../../../entity/query';
import { copyToClipboard } from '../../../shared/lib';
import { LOCAL_TIMEZONE, formatInTimezone, getUserTimeFormatWithMs } from '../../../shared/time';
import { LevelPillComponent } from './LevelPillComponent';

export type DetailViewMode = 'json' | 'table';

interface Props {
  log: LogItem;
  /** side/below: a card next to the results; inline: inside the clicked row. */
  variant: 'side' | 'below' | 'inline';
  viewMode: DetailViewMode;
  onViewModeChange: (mode: DetailViewMode) => void;
  onClose: () => void;
  onAddFieldToQuery?: (fieldName: string, fieldValue: string) => void;
  /** Display timezone, same selector as QueryResultsComponent's View settings. */
  timezone?: string;
  /** "Hide" next to a field value: hide logs with that value. */
  onExcludeFieldFromQuery?: (fieldName: string, fieldValue: string) => void;
  /** "Show logs around" - opens the context panel for this log. */
  onShowContext?: (log: LogItem) => void;
  /** "Open trace" - shows the trace this log belongs to (Traces page). */
  onOpenTrace?: (traceId: string, timestamp: string) => void;
  /** "Same pattern" - show only logs with this log's message pattern. */
  onIncludePattern?: (patternHash: string) => void;
}

interface DetailRow {
  key: string;
  value: string;
  isCustomField: boolean;
}

/** A log's full record as raw JSON or a click-to-filter table, beside or inside the results. */
export const LogDetailPanelComponent = ({
  log,
  variant,
  viewMode,
  onViewModeChange,
  onClose,
  onAddFieldToQuery,
  timezone = LOCAL_TIMEZONE,
  onExcludeFieldFromQuery,
  onShowContext,
  onOpenTrace,
  onIncludePattern,
}: Props): React.JSX.Element => {
  const [isCopying, setIsCopying] = useState(false);

  const timeFormat = getUserTimeFormatWithMs();
  const formattedTimestamp = formatInTimezone(log.timestamp, timeFormat.format, timezone);

  const jsonText = JSON.stringify(
    {
      timestamp: log.timestamp,
      level: log.level,
      message: log.message,
      clientIp: log.clientIp,
      ...(log.traceId ? { traceId: log.traceId } : {}),
      ...(log.spanId ? { spanId: log.spanId } : {}),
      ...log.fields,
    },
    null,
    2,
  );

  const handleCopy = async () => {
    setIsCopying(true);
    try {
      await copyToClipboard(jsonText);
    } finally {
      setTimeout(() => setIsCopying(false), 300);
    }
  };

  const rows: DetailRow[] = [
    { key: 'timestamp', value: formattedTimestamp, isCustomField: false },
    { key: 'level', value: log.level, isCustomField: false },
    { key: 'message', value: log.message, isCustomField: false },
    ...(log.clientIp ? [{ key: 'client_ip', value: log.clientIp, isCustomField: false }] : []),
    // Filterable like a field (trace_id:<id> finds the trace's other logs).
    ...(log.traceId ? [{ key: 'trace_id', value: log.traceId, isCustomField: true }] : []),
    ...(log.spanId ? [{ key: 'span_id', value: log.spanId, isCustomField: true }] : []),
    ...Object.entries(log.fields ?? {}).map(([key, value]) => ({
      key,
      value: typeof value === 'string' ? value : JSON.stringify(value),
      isCustomField: true,
    })),
  ];

  const isInline = variant === 'inline';

  const actions = (
    <div className="flex flex-wrap items-center gap-2">
      {log.traceId && onOpenTrace && (
        <Button
          type="primary"
          size={isInline ? 'small' : 'middle'}
          icon={<ApartmentOutlined />}
          onClick={() => onOpenTrace(log.traceId as string, log.timestamp)}
        >
          Open trace
        </Button>
      )}
      {onShowContext && (
        <Button
          size={isInline ? 'small' : 'middle'}
          icon={<ColumnHeightOutlined />}
          onClick={() => onShowContext(log)}
        >
          Show logs around
        </Button>
      )}
      {log.patternHash && onIncludePattern && (
        <Tooltip title="Show only logs like this (same message pattern)">
          <Button
            size={isInline ? 'small' : 'middle'}
            onClick={() => onIncludePattern(log.patternHash as string)}
          >
            Same pattern
          </Button>
        </Tooltip>
      )}
    </div>
  );

  const table = (
    <div className="flex flex-col">
      {rows.map((row) => (
        <div
          key={row.key}
          className={`grid items-start gap-2 border-b border-gray-100 py-1 text-xs last:border-b-0 dark:border-gray-700 ${
            variant === 'below'
              ? 'grid-cols-[minmax(110px,22%)_minmax(0,1fr)_auto]'
              : 'grid-cols-[minmax(96px,36%)_minmax(0,1fr)_auto]'
          }`}
        >
          <span className="truncate pt-0.5 font-mono font-medium text-gray-500 dark:text-gray-400">
            {row.key}
          </span>
          {row.isCustomField && onAddFieldToQuery ? (
            <>
              <button
                type="button"
                onClick={() => onAddFieldToQuery(row.key, row.value)}
                className="hover:bg-accent-50 dark:hover:bg-accent-900/40 min-w-0 cursor-pointer rounded px-1 py-0.5 text-left font-mono text-xs break-all whitespace-pre-wrap text-gray-800 dark:text-gray-100"
                title={`Show only: ${row.key}:${row.value}`}
              >
                {row.value}
              </button>
              <span className="flex gap-0.5">
                <button
                  type="button"
                  onClick={() => onAddFieldToQuery(row.key, row.value)}
                  className="text-accent-600 hover:bg-accent-50 dark:text-accent-400 dark:hover:bg-accent-900/40 flex h-6 w-6 cursor-pointer items-center justify-center rounded-md"
                  title={`Show only: ${row.key}:${row.value}`}
                  aria-label={`Show only logs where ${row.key} is ${row.value}`}
                >
                  <PlusCircleOutlined />
                </button>
                {onExcludeFieldFromQuery && (
                  <button
                    type="button"
                    onClick={() => onExcludeFieldFromQuery(row.key, row.value)}
                    className="flex h-6 w-6 cursor-pointer items-center justify-center rounded-md text-gray-400 hover:bg-red-50 hover:text-red-600 dark:text-gray-500 dark:hover:bg-red-900/30 dark:hover:text-red-400"
                    title={`Hide: ${row.key}:${row.value}`}
                    aria-label={`Hide logs where ${row.key} is ${row.value}`}
                  >
                    <MinusCircleOutlined />
                  </button>
                )}
              </span>
            </>
          ) : (
            <>
              <span className="min-w-0 px-1 py-0.5 font-mono text-xs break-all whitespace-pre-wrap text-gray-800 dark:text-gray-100">
                {row.value}
              </span>
              <span />
            </>
          )}
        </div>
      ))}
    </div>
  );

  const json = (
    <div className="space-y-2">
      <div className="flex justify-end">
        <Button size="small" icon={<CopyOutlined />} loading={isCopying} onClick={handleCopy}>
          Copy
        </Button>
      </div>
      <div
        className={`overflow-auto rounded-xl border border-gray-200 dark:border-gray-700 ${isInline ? 'max-h-80' : 'max-h-[70vh]'}`}
      >
        <SyntaxHighlighter
          language="json"
          style={tomorrow}
          customStyle={{ margin: 0, fontSize: '12px' }}
        >
          {jsonText}
        </SyntaxHighlighter>
      </div>
    </div>
  );

  const viewSwitch = (
    <Segmented
      size="small"
      value={viewMode}
      onChange={(v) => onViewModeChange(v as DetailViewMode)}
      options={[
        { label: 'JSON', value: 'json' },
        { label: 'Table', value: 'table' },
      ]}
    />
  );

  if (isInline) {
    return (
      <div
        className="mt-1.5 mb-1 flex cursor-default flex-col gap-2 rounded-xl border border-gray-200 bg-gray-50 p-3 font-sans text-[13px] dark:border-gray-700 dark:bg-gray-900/60"
        // Clicks inside the details must not toggle the row.
        onClick={(e) => e.stopPropagation()}
      >
        <div className="flex flex-wrap items-center justify-between gap-2">
          <div className="flex flex-wrap items-center gap-2">
            {viewSwitch}
            <span className="font-mono text-xs text-gray-500 dark:text-gray-400">
              Log at {formattedTimestamp}
            </span>
          </div>
          <div className="flex items-center gap-2">
            {actions}
            <Button
              type="text"
              size="small"
              icon={<CloseOutlined />}
              onClick={onClose}
              aria-label="Close details"
            />
          </div>
        </div>
        {viewMode === 'json' ? json : table}
      </div>
    );
  }

  return (
    <aside
      aria-label="Log details"
      className="flex min-w-0 flex-col gap-3 rounded-2xl border border-gray-200 bg-white p-4 dark:border-gray-700 dark:bg-gray-800"
    >
      <div className="flex items-start justify-between gap-2">
        <div className="min-w-0">
          <h2 className="m-0 text-[15px] font-semibold text-gray-900 dark:text-gray-50">
            Log at {formattedTimestamp}
          </h2>
          <p className="m-0 mt-0.5 text-xs text-gray-500 dark:text-gray-400">
            Click a value to show only logs like it
          </p>
        </div>
        <span className="flex shrink-0 items-center gap-2">
          <LevelPillComponent level={log.level} />
          <Tooltip title="Close details (Esc)">
            <Button
              type="text"
              size="small"
              icon={<CloseOutlined />}
              onClick={onClose}
              aria-label="Close details"
            />
          </Tooltip>
        </span>
      </div>
      <div className="max-h-48 overflow-auto rounded-xl bg-gray-50 p-3 font-mono text-xs leading-relaxed break-all whitespace-pre-wrap text-gray-800 dark:bg-gray-900/60 dark:text-gray-100">
        {log.message}
      </div>
      {actions}
      <div>{viewSwitch}</div>
      {viewMode === 'json' ? json : table}
    </aside>
  );
};
