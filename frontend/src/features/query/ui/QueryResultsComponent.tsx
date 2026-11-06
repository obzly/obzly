import {
  CheckOutlined,
  ColumnHeightOutlined,
  CopyOutlined,
  DownloadOutlined,
  LoadingOutlined,
  MinusOutlined,
  PlusOutlined,
  SettingOutlined,
  TableOutlined,
} from '@ant-design/icons';
import { useVirtualizer } from '@tanstack/react-virtual';
import { Button, Checkbox, Dropdown, Input, Popover, Select, Spin, Switch, Tooltip } from 'antd';
import dayjs from 'dayjs';
import React, { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState } from 'react';

import { type LogContextScope, type LogItem } from '../../../entity/query';
import { copyToClipboard, toCSV } from '../../../shared/lib';
import {
  LOCAL_TIMEZONE,
  formatInTimezone,
  getAvailableTimezones,
  getTimezoneLabel,
  getUserTimeFormatWithMs,
  useDisplayTimezone,
} from '../../../shared/time';
import { levelColor } from '../lib/logLevels';
import { LogContextPanelComponent } from './LogContextPanelComponent';
import { LogDetailDrawerComponent } from './LogDetailDrawerComponent';

const STORAGE_KEY = 'obzly-message-length';
const COLUMN_WIDTHS_STORAGE_KEY = 'obzly-query-results-column-widths';
// Versioned: bump when the defaults change, since the stored list is
// written back on every visit.
const VISIBLE_COLUMNS_STORAGE_KEY = 'obzly-query-results-columns-v3';
const WRAP_STORAGE_KEY = 'obzly-query-results-wrap';
// With wrap on the whole message shows; only a pathological payload is cut.
const WRAP_MAX_MESSAGE_CHARS = 10_000;
const MIN_COLUMN_WIDTH = 50;
const DEFAULT_FIELD_COLUMN_WIDTH = 160;

/** A built-in column, or `field:<key>` for any key of a log's `fields`. */
type ColumnId = 'timestamp' | 'level' | 'service' | 'message' | `field:${string}`;

const BUILT_IN_COLUMNS: { id: ColumnId; label: string }[] = [
  { id: 'timestamp', label: 'Time' },
  { id: 'level', label: 'Level' },
  { id: 'service', label: 'Service' },
  { id: 'message', label: 'Message' },
];
const DEFAULT_VISIBLE_COLUMNS: ColumnId[] = ['timestamp', 'level', 'message'];
const DEFAULT_COLUMN_WIDTHS: Record<string, number> = { timestamp: 175, level: 85, service: 160 };

// OTLP and plain REST ingest name the service field differently.
const SERVICE_FIELD_KEYS = ['service_name', 'service'];

const readStorage = <T,>(key: string, parse: (raw: string) => T | undefined, fallback: T): T => {
  try {
    const raw = localStorage.getItem(key);
    if (raw !== null) {
      const parsed = parse(raw);
      if (parsed !== undefined) return parsed;
    }
  } catch {
    /* unavailable or malformed - use the fallback */
  }
  return fallback;
};

const writeStorage = (key: string, value: string) => {
  try {
    localStorage.setItem(key, value);
  } catch {
    /* unavailable - the preference just won't persist */
  }
};

const getStoredMessageLength = (): number =>
  readStorage(
    STORAGE_KEY,
    (raw) => {
      const parsed = parseInt(raw, 10);
      return !isNaN(parsed) && parsed >= 10 && parsed <= 1000 ? parsed : undefined;
    },
    // The input's max.
    1000,
  );

const getStoredColumnWidths = (): Record<string, number> =>
  readStorage(
    COLUMN_WIDTHS_STORAGE_KEY,
    (raw) => {
      const parsed: unknown = JSON.parse(raw);
      if (typeof parsed !== 'object' || parsed === null) return undefined;
      const widths: Record<string, number> = {};
      for (const [key, value] of Object.entries(parsed)) {
        if (typeof value === 'number' && value >= MIN_COLUMN_WIDTH) widths[key] = value;
      }
      return { ...DEFAULT_COLUMN_WIDTHS, ...widths };
    },
    DEFAULT_COLUMN_WIDTHS,
  );

const getStoredVisibleColumns = (): ColumnId[] =>
  readStorage(
    VISIBLE_COLUMNS_STORAGE_KEY,
    (raw) => {
      const parsed: unknown = JSON.parse(raw);
      if (!Array.isArray(parsed)) return undefined;
      const builtIns = new Set(BUILT_IN_COLUMNS.map((c) => c.id as string));
      const ids = parsed.filter(
        (id): id is ColumnId =>
          typeof id === 'string' && (builtIns.has(id) || id.startsWith('field:')),
      );
      return ids.length > 0 ? ids : undefined;
    },
    DEFAULT_VISIBLE_COLUMNS,
  );

const fieldValue = (log: LogItem, key: string): string => {
  const value = log.fields?.[key];
  if (value === undefined || value === null) return '';
  return typeof value === 'string' ? value : JSON.stringify(value);
};

const serviceOf = (log: LogItem): string => {
  for (const key of SERVICE_FIELD_KEYS) {
    const value = fieldValue(log, key);
    if (value) return value;
  }
  return '';
};

const columnLabel = (id: ColumnId): string =>
  BUILT_IN_COLUMNS.find((c) => c.id === id)?.label ?? id.slice('field:'.length);

const isTypingTarget = (target: EventTarget | null): boolean => {
  if (!(target instanceof HTMLElement)) return false;
  return (
    target.isContentEditable ||
    ['INPUT', 'TEXTAREA', 'SELECT'].includes(target.tagName) ||
    target.closest('[role="combobox"], .monaco-editor') !== null
  );
};

const LEVEL_BADGE_CLASSES: Record<string, string> = {
  ERROR: 'bg-red-100 text-red-800 border-red-200',
  WARN: 'bg-yellow-100 text-yellow-800 border-yellow-200',
  INFO: 'bg-blue-100 text-blue-800 border-blue-200',
  DEBUG:
    'bg-gray-100 dark:bg-gray-700 text-gray-800 dark:text-gray-100 border-gray-200 dark:border-gray-700',
  TRACE: 'bg-purple-100 text-purple-800 border-purple-200',
  FATAL: 'bg-red-200 text-red-900 border-red-300',
  CRITICAL: 'bg-red-200 text-red-900 border-red-300',
};

interface Props {
  queryResults: LogItem[];
  totalResults: number;
  hasExecuted: boolean;
  isExecuting: boolean;
  hasMoreResults: boolean;
  onLoadMore: () => void;
  onAddFieldToQuery?: (fieldName: string, fieldValue: string) => void;
  onExcludeFieldFromQuery?: (fieldName: string, fieldValue: string) => void;
  /**
   * The timestamp the histogram should mark: the row under the mouse, or
   * else the row whose detail drawer is open; null when neither.
   */
  onCursorTimeChange?: (timestamp: string | null) => void;
  /** Row hover "+": show only logs with this row's message pattern. */
  onIncludePattern?: (patternHash: string) => void;
  /** Row hover "−": hide logs with this row's message pattern. */
  onExcludePattern?: (patternHash: string) => void;
  /** Set to offer "Show logs around" (LogContextPanelComponent) on rows. */
  projectId?: string;
  /** The context panel's "Open as search". */
  onOpenContextAsSearch?: (scope: LogContextScope[], from: dayjs.Dayjs, to: dayjs.Dayjs) => void;
  /** "Open trace" on a log with a trace id. */
  onOpenTrace?: (traceId: string, timestamp: string) => void;
}

const downloadFile = (name: string, content: string, type: string) => {
  const url = URL.createObjectURL(new Blob([content], { type }));
  const a = document.createElement('a');
  a.href = url;
  a.download = name;
  document.body.appendChild(a);
  a.click();
  a.remove();
  setTimeout(() => URL.revokeObjectURL(url), 1000);
};

/** The logs results table: virtualized, infinite scroll, per-browser column choices. */
export const QueryResultsComponent = ({
  queryResults,
  totalResults,
  hasExecuted,
  isExecuting,
  hasMoreResults,
  onLoadMore,
  onAddFieldToQuery,
  onExcludeFieldFromQuery,
  onCursorTimeChange,
  onIncludePattern,
  onExcludePattern,
  projectId,
  onOpenContextAsSearch,
  onOpenTrace,
}: Props): React.JSX.Element | null => {
  const [messageLength, setMessageLength] = useState<number>(getStoredMessageLength());
  const [selectedLog, setSelectedLog] = useState<LogItem | null>(null);
  const [contextLog, setContextLog] = useState<LogItem | null>(null);
  const [hoveredTimestamp, setHoveredTimestamp] = useState<string | null>(null);
  const [timezone, setTimezone] = useDisplayTimezone();
  const [columnWidths, setColumnWidths] = useState<Record<string, number>>(getStoredColumnWidths);
  const [visibleColumns, setVisibleColumns] = useState<ColumnId[]>(getStoredVisibleColumns);
  const [copiedLogId, setCopiedLogId] = useState<string | null>(null);
  const [wrap, setWrap] = useState<boolean>(() =>
    readStorage(WRAP_STORAGE_KEY, (raw) => raw === 'true', false),
  );
  const [columnSearch, setColumnSearch] = useState('');
  const resizingColumnKey = useRef<string | null>(null);

  const timeFormat = useMemo(() => getUserTimeFormatWithMs(), []);

  const timezoneOptions = useMemo(
    () =>
      [LOCAL_TIMEZONE, ...getAvailableTimezones()].map((tz) => ({
        value: tz,
        label: getTimezoneLabel(tz),
      })),
    [],
  );

  const handleTimezoneChange = (value: string) => setTimezone(value);

  const isLoadingMore = useRef(false);
  const containerRef = useRef<HTMLDivElement>(null);

  const handleScroll = useCallback(() => {
    if (isLoadingMore.current || !hasMoreResults || isExecuting) {
      return;
    }

    const scrollContainer = containerRef.current?.closest('.overflow-y-auto') as HTMLElement;
    if (!scrollContainer) {
      return;
    }

    // Prefetch 1.5 screens ahead so a fast scroll rarely hits the end.
    const { scrollTop, scrollHeight, clientHeight } = scrollContainer;
    if (scrollHeight - scrollTop - clientHeight < Math.max(300, clientHeight * 1.5)) {
      isLoadingMore.current = true;
      onLoadMore();
    }
  }, [hasMoreResults, isExecuting, onLoadMore]);

  // The rows scroll with the page and start below the search bar and
  // histogram; the virtualizer needs that offset (scrollMargin) or it
  // renders the wrong slice. Re-measured on render and on resize.
  const listRef = useRef<HTMLDivElement>(null);
  const [scrollMargin, setScrollMargin] = useState(0);
  const getScrollElement = useCallback(
    () => containerRef.current?.closest('.overflow-y-auto') as HTMLElement | null,
    [],
  );
  const updateScrollMargin = useCallback(() => {
    const list = listRef.current;
    const scroller = getScrollElement();
    if (!list || !scroller) return;
    const margin =
      list.getBoundingClientRect().top - scroller.getBoundingClientRect().top + scroller.scrollTop;
    setScrollMargin((prev) => (Math.abs(prev - margin) > 0.5 ? margin : prev));
  }, [getScrollElement]);

  useLayoutEffect(() => {
    updateScrollMargin();
  });

  useEffect(() => {
    const scroller = getScrollElement();
    if (!scroller || typeof ResizeObserver === 'undefined') return;
    const observer = new ResizeObserver(() => updateScrollMargin());
    for (const child of Array.from(scroller.children)) observer.observe(child);
    if (containerRef.current) observer.observe(containerRef.current);
    return () => observer.disconnect();
  }, [getScrollElement, updateScrollMargin, hasExecuted]);

  // Rows are measured dynamically since wrapped rows vary in height.
  // eslint-disable-next-line react-hooks/incompatible-library
  const rowVirtualizer = useVirtualizer({
    count: queryResults.length,
    getScrollElement,
    estimateSize: () => 26,
    overscan: 10,
    scrollMargin,
  });

  // Toggling wrap changes every row's height - drop cached measurements.
  useEffect(() => {
    rowVirtualizer.measure();
  }, [wrap, visibleColumns, rowVirtualizer]);

  // Re-check after each load: parked at the bottom, the browser fires no
  // more scroll events, so loading would stall.
  useEffect(() => {
    if (isExecuting) return;
    isLoadingMore.current = false;
    const frame = requestAnimationFrame(() => handleScroll());
    return () => cancelAnimationFrame(frame);
  }, [isExecuting, queryResults.length, handleScroll]);

  useEffect(() => {
    const scrollContainer = containerRef.current?.closest('.overflow-y-auto') as HTMLElement;
    if (!scrollContainer) {
      return;
    }

    scrollContainer.addEventListener('scroll', handleScroll);
    return () => scrollContainer.removeEventListener('scroll', handleScroll);
  }, [handleScroll]);

  useEffect(() => writeStorage(STORAGE_KEY, messageLength.toString()), [messageLength]);
  useEffect(
    () => writeStorage(COLUMN_WIDTHS_STORAGE_KEY, JSON.stringify(columnWidths)),
    [columnWidths],
  );
  useEffect(
    () => writeStorage(VISIBLE_COLUMNS_STORAGE_KEY, JSON.stringify(visibleColumns)),
    [visibleColumns],
  );
  useEffect(() => writeStorage(WRAP_STORAGE_KEY, String(wrap)), [wrap]);

  // `W` toggles wrap, unless the user is typing somewhere.
  useEffect(() => {
    if (!hasExecuted) return;
    const onKeyDown = (e: KeyboardEvent) => {
      if (e.key.toLowerCase() !== 'w' || e.ctrlKey || e.metaKey || e.altKey) return;
      if (isTypingTarget(e.target)) return;
      setWrap((w) => !w);
    };
    document.addEventListener('keydown', onKeyDown);
    return () => document.removeEventListener('keydown', onKeyDown);
  }, [hasExecuted]);

  // Cursor for the histogram: hovered row wins, else the open row.
  const cursorTime = hoveredTimestamp ?? selectedLog?.timestamp ?? null;
  useEffect(() => {
    onCursorTimeChange?.(cursorTime);
  }, [cursorTime, onCursorTimeChange]);

  const availableFieldKeys = useMemo(() => {
    const keys = new Set<string>();
    for (const log of queryResults) {
      for (const key of Object.keys(log.fields ?? {})) keys.add(key);
    }
    return [...keys].sort();
  }, [queryResults]);

  // Hide an all-blank Service column without forgetting the choice.
  const hasServiceValues = useMemo(
    () => queryResults.some((log) => serviceOf(log)),
    [queryResults],
  );
  const shownColumns = useMemo(() => {
    const shown = visibleColumns.filter((id) => id !== 'service' || hasServiceValues);
    return shown.length > 0 ? shown : visibleColumns;
  }, [visibleColumns, hasServiceValues]);

  const toggleColumn = (id: ColumnId, checked: boolean) => {
    setVisibleColumns((prev) => {
      if (checked) {
        if (prev.includes(id)) return prev;
        // Keep Message last: new columns go just before it.
        const messageIndex = prev.indexOf('message');
        if (messageIndex === -1) return [...prev, id];
        return [...prev.slice(0, messageIndex), id, ...prev.slice(messageIndex)];
      }
      const next = prev.filter((c) => c !== id);
      return next.length > 0 ? next : prev; // never zero columns
    });
  };

  // Message fills the remaining width, so it has no resize handle.
  const handleColumnResizeStart = useCallback(
    (columnKey: string) => (mouseDownEvent: React.MouseEvent) => {
      mouseDownEvent.preventDefault();
      resizingColumnKey.current = columnKey;
      const startX = mouseDownEvent.clientX;
      const startWidth = columnWidths[columnKey] ?? DEFAULT_FIELD_COLUMN_WIDTH;

      const handleMouseMove = (moveEvent: MouseEvent) => {
        const key = resizingColumnKey.current;
        if (!key) return;
        const newWidth = Math.max(MIN_COLUMN_WIDTH, startWidth + (moveEvent.clientX - startX));
        setColumnWidths((prev) => ({ ...prev, [key]: newWidth }));
      };

      const handleMouseUp = () => {
        resizingColumnKey.current = null;
        document.removeEventListener('mousemove', handleMouseMove);
        document.removeEventListener('mouseup', handleMouseUp);
      };

      document.addEventListener('mousemove', handleMouseMove);
      document.addEventListener('mouseup', handleMouseUp);
    },
    [columnWidths],
  );

  if (!hasExecuted) {
    return null;
  }

  const widthOf = (id: ColumnId) => columnWidths[id] ?? DEFAULT_FIELD_COLUMN_WIDTH;

  const renderCell = (id: ColumnId, log: LogItem): React.ReactNode => {
    switch (id) {
      case 'timestamp':
        return (
          <Tooltip title={dayjs(log.timestamp).fromNow()}>
            <span className="whitespace-nowrap text-gray-600 dark:text-gray-300">
              {formatInTimezone(log.timestamp, timeFormat.format, timezone)}
            </span>
          </Tooltip>
        );
      case 'level': {
        const colorClass = LEVEL_BADGE_CLASSES[log.level.toUpperCase()] ?? LEVEL_BADGE_CLASSES.INFO;
        return (
          <span
            className={`inline-block rounded border px-1 text-xs leading-4 font-medium ${colorClass}`}
          >
            {log.level}
          </span>
        );
      }
      case 'service':
        return <span className="text-gray-700 dark:text-gray-200">{serviceOf(log)}</span>;
      case 'message': {
        const limit = wrap ? WRAP_MAX_MESSAGE_CHARS : messageLength;
        const truncated = log.message.length > limit;
        const text = truncated ? `${log.message.substring(0, limit)}...` : log.message;
        return (
          <span className="text-gray-900 dark:text-gray-100">
            {text}
            {truncated && wrap && (
              <span className="ml-1 text-emerald-600 hover:text-emerald-700">(view details)</span>
            )}
          </span>
        );
      }
      default:
        return (
          <span className="text-gray-700 dark:text-gray-200">
            {fieldValue(log, id.slice('field:'.length))}
          </span>
        );
    }
  };

  // Message (if shown) flexes; everything else is fixed-width.
  const cellClass = (id: ColumnId) => {
    const lineClass =
      id === 'message' && wrap
        ? 'whitespace-pre-wrap break-all'
        : 'overflow-hidden text-ellipsis whitespace-nowrap';
    return `${id === 'message' ? 'min-w-0 flex-1' : 'shrink-0'} ${lineClass}`;
  };
  const cellStyle = (id: ColumnId) => (id === 'message' ? undefined : { width: widthOf(id) });

  const needle = columnSearch.trim().toLowerCase();
  const pickerFieldKeys = [
    // Selected field columns first, even if absent from these results.
    ...visibleColumns.filter((c) => c.startsWith('field:')).map((c) => c.slice(6)),
    ...availableFieldKeys.filter((k) => !visibleColumns.includes(`field:${k}`)),
  ].filter((k) => !needle || k.toLowerCase().includes(needle));

  const columnPicker = (
    <div className="w-64">
      <div className="mb-1 text-xs font-medium text-gray-500 uppercase dark:text-gray-300">
        Columns
      </div>
      <div className="flex flex-col gap-0.5">
        {BUILT_IN_COLUMNS.map((c) => (
          <Checkbox
            key={c.id}
            checked={visibleColumns.includes(c.id)}
            onChange={(e) => toggleColumn(c.id, e.target.checked)}
          >
            <span className="text-xs">
              {c.label}
              {c.id === 'service' && visibleColumns.includes('service') && !hasServiceValues && (
                <span className="ml-1 text-gray-400">(hidden - no values in these results)</span>
              )}
            </span>
          </Checkbox>
        ))}
      </div>
      <div className="mt-2 mb-1 text-xs font-medium text-gray-500 uppercase dark:text-gray-300">
        Fields
      </div>
      <Input
        size="small"
        allowClear
        placeholder="Search fields"
        value={columnSearch}
        onChange={(e) => setColumnSearch(e.target.value)}
      />
      <div className="mt-1 flex max-h-56 flex-col gap-0.5 overflow-y-auto">
        {pickerFieldKeys.length === 0 ? (
          <span className="py-1 text-xs text-gray-400">
            {availableFieldKeys.length === 0 ? 'No fields in these results' : 'No matching fields'}
          </span>
        ) : (
          pickerFieldKeys.map((key) => {
            const id: ColumnId = `field:${key}`;
            return (
              <Checkbox
                key={key}
                checked={visibleColumns.includes(id)}
                onChange={(e) => toggleColumn(id, e.target.checked)}
              >
                <span className="font-mono text-xs break-all">{key}</span>
              </Checkbox>
            );
          })
        )}
      </div>
      <Button
        size="small"
        type="link"
        className="mt-1 !px-0"
        onClick={() => {
          setVisibleColumns(DEFAULT_VISIBLE_COLUMNS);
          setColumnWidths(DEFAULT_COLUMN_WIDTHS);
        }}
      >
        Reset columns
      </Button>
    </div>
  );

  // Only loaded rows: the full result set could be millions.
  const exportRows = (format: 'csv' | 'json') => {
    const stamp = new Date().toISOString().replace(/[:.]/g, '-');
    if (format === 'json') {
      downloadFile(`logs-${stamp}.json`, JSON.stringify(queryResults, null, 2), 'application/json');
      return;
    }
    const header = shownColumns.map((id) => columnLabel(id));
    const lines = queryResults.map((log) =>
      shownColumns.map((id) => {
        switch (id) {
          case 'timestamp':
            return log.timestamp;
          case 'level':
            return log.level;
          case 'service':
            return serviceOf(log);
          case 'message':
            return log.message;
          default:
            return fieldValue(log, id.slice('field:'.length));
        }
      }),
    );
    // toCSV defuses spreadsheet formulas: log fields are attacker-controlled.
    downloadFile(`logs-${stamp}.csv`, toCSV([header, ...lines]), 'text/csv');
  };

  const displaySettings = (
    <div className="flex w-64 flex-col gap-3 text-xs">
      <div>
        <div className="mb-1 text-gray-500 dark:text-gray-300">Timezone</div>
        <Select
          size="small"
          showSearch
          value={timezone}
          onChange={handleTimezoneChange}
          options={timezoneOptions}
          className="w-full"
          popupMatchSelectWidth={false}
          filterOption={(input, option) =>
            (option?.label ?? '').toLowerCase().includes(input.toLowerCase())
          }
        />
      </div>
      <div>
        <label htmlFor="messageLength" className="mb-1 block text-gray-500 dark:text-gray-300">
          Message length (characters)
        </label>
        <input
          id="messageLength"
          type="number"
          value={messageLength}
          onChange={(e) => setMessageLength(Math.max(1, parseInt(e.target.value)))}
          className="w-24 rounded border border-gray-300 px-1 py-0.5 text-xs focus:border-emerald-600 focus:ring-1 focus:ring-emerald-600 focus:outline-none dark:border-gray-600"
          min="1"
          max="1000"
        />
      </div>
    </div>
  );

  return (
    <div
      ref={containerRef}
      className="w-full rounded-lg border border-gray-200 bg-white shadow-sm dark:border-gray-700 dark:bg-gray-800"
    >
      <div className="border-b border-gray-200 px-3 py-1.5 dark:border-gray-700">
        <div className="flex flex-wrap items-center justify-between gap-2">
          <span className="text-xs font-normal text-gray-500 dark:text-gray-300">
            {isExecuting && queryResults.length === 0 ? (
              <Spin indicator={<LoadingOutlined spin />} size="small" />
            ) : (
              `${queryResults.length.toLocaleString()}${totalResults > queryResults.length ? `+ of ${totalResults.toLocaleString()}` : totalResults < 0 && hasMoreResults ? '+' : ''} results${queryResults.length > 0 ? ' loaded' : ' found'}`
            )}
          </span>
          <div className="flex flex-wrap items-center gap-2">
            <Tooltip title="Wrap long messages (W)">
              <label className="flex cursor-pointer items-center gap-1.5 text-xs text-gray-500 dark:text-gray-300">
                Wrap
                <kbd className="rounded border border-gray-300 px-1 font-mono text-[10px] leading-4 dark:border-gray-600">
                  W
                </kbd>
                <Switch size="small" checked={wrap} onChange={setWrap} />
              </label>
            </Tooltip>

            <Popover content={columnPicker} trigger="click" placement="bottomRight">
              <Tooltip title="Choose columns">
                <Button size="small" icon={<TableOutlined />} aria-label="Choose columns" />
              </Tooltip>
            </Popover>

            <Dropdown
              trigger={['click']}
              placement="bottomRight"
              disabled={queryResults.length === 0}
              menu={{
                items: [
                  {
                    key: 'csv',
                    label: `CSV - ${queryResults.length.toLocaleString()} loaded rows`,
                  },
                  {
                    key: 'json',
                    label: `JSON - ${queryResults.length.toLocaleString()} loaded rows`,
                  },
                ],
                onClick: ({ key }) => exportRows(key as 'csv' | 'json'),
              }}
            >
              <Tooltip title="Export loaded results">
                <Button size="small" icon={<DownloadOutlined />} aria-label="Export results" />
              </Tooltip>
            </Dropdown>

            <Popover content={displaySettings} trigger="click" placement="bottomRight">
              <Tooltip title="Display settings (timezone, message length)">
                <Button size="small" icon={<SettingOutlined />} aria-label="Display settings" />
              </Tooltip>
            </Popover>
          </div>
        </div>
      </div>

      <div className="p-3">
        {isExecuting && queryResults.length === 0 ? (
          <div className="flex h-32 items-center justify-center">
            <Spin indicator={<LoadingOutlined spin />} />
            <span className="ml-2 text-sm">Executing query...</span>
          </div>
        ) : queryResults.length === 0 ? (
          <div className="flex h-20 items-center justify-center text-sm text-gray-500 dark:text-gray-300">
            No logs found matching your query.
          </div>
        ) : (
          <div className="space-y-1">
            <div className="sticky top-0 z-10 flex gap-3 border-b border-l-[3px] border-gray-200 border-l-transparent bg-white pt-1 pr-[88px] pb-1 pl-2 text-xs font-medium text-gray-700 dark:border-gray-700 dark:border-l-transparent dark:bg-gray-800 dark:text-gray-200">
              {shownColumns.map((id) => (
                <div
                  key={id}
                  className={`relative truncate pr-2 ${id === 'message' ? 'min-w-0 flex-1' : 'shrink-0'}`}
                  style={cellStyle(id)}
                  title={columnLabel(id)}
                >
                  {columnLabel(id)}
                  {id !== 'message' && (
                    <div
                      onMouseDown={handleColumnResizeStart(id)}
                      className="absolute top-0 right-0 z-10 h-full w-1.5 cursor-col-resize hover:bg-emerald-400"
                    />
                  )}
                </div>
              ))}
            </div>

            <div
              ref={listRef}
              style={{ position: 'relative', height: rowVirtualizer.getTotalSize() }}
              onMouseLeave={() => setHoveredTimestamp(null)}
            >
              {rowVirtualizer.getVirtualItems().map((virtualRow) => {
                const log = queryResults[virtualRow.index];
                const isSelected = selectedLog?.id === log.id;

                return (
                  <div
                    key={log.id}
                    ref={rowVirtualizer.measureElement}
                    data-index={virtualRow.index}
                    style={{
                      position: 'absolute',
                      top: 0,
                      left: 0,
                      width: '100%',
                      // virtualRow.start includes scrollMargin; rows sit in the list.
                      transform: `translateY(${virtualRow.start - scrollMargin}px)`,
                      borderLeftColor: levelColor(log.level),
                    }}
                    className={`group flex cursor-pointer items-start gap-3 border-l-[3px] pr-[88px] pl-2 ${wrap ? 'py-1' : 'py-0.5'} !font-mono text-xs hover:bg-gray-100 dark:hover:bg-gray-700 ${
                      isSelected
                        ? 'bg-emerald-50 dark:bg-emerald-900/30'
                        : virtualRow.index % 2 === 1
                          ? 'bg-gray-50 dark:bg-gray-900/60'
                          : 'dark:bg-gray-900'
                    }`}
                    onClick={() => setSelectedLog(log)}
                    onMouseEnter={() => setHoveredTimestamp(log.timestamp)}
                  >
                    {shownColumns.map((id) => (
                      <div key={id} className={cellClass(id)} style={cellStyle(id)}>
                        {renderCell(id, log)}
                      </div>
                    ))}
                    {/* In the reserved right gutter (pr-[88px]) so they never cover the message. */}
                    <div className="absolute top-0 right-1 hidden items-center gap-0.5 pt-0.5 group-hover:flex">
                      <Tooltip title={copiedLogId === log.id ? 'Copied' : 'Copy message'}>
                        <Button
                          size="small"
                          className="!h-[18px] !w-[18px] !min-w-0 !p-0 text-[10px]"
                          icon={copiedLogId === log.id ? <CheckOutlined /> : <CopyOutlined />}
                          aria-label="Copy message"
                          onClick={async (e) => {
                            e.stopPropagation();
                            if (await copyToClipboard(log.message)) {
                              setCopiedLogId(log.id);
                              window.setTimeout(
                                () => setCopiedLogId((id) => (id === log.id ? null : id)),
                                1500,
                              );
                            }
                          }}
                        />
                      </Tooltip>
                      {projectId && (
                        <Tooltip title="Show logs around this one (same source)">
                          <Button
                            size="small"
                            className="!h-[18px] !w-[18px] !min-w-0 !p-0 text-[10px]"
                            icon={<ColumnHeightOutlined />}
                            aria-label="Show logs around this one"
                            onClick={(e) => {
                              e.stopPropagation();
                              setContextLog(log);
                            }}
                          />
                        </Tooltip>
                      )}
                      {log.patternHash && (
                        <>
                          {onIncludePattern && (
                            <Tooltip title="Show only logs like this (same message pattern)">
                              <Button
                                size="small"
                                className="!h-[18px] !w-[18px] !min-w-0 !p-0 text-[10px]"
                                icon={<PlusOutlined />}
                                aria-label="Show only logs like this"
                                onClick={(e) => {
                                  e.stopPropagation();
                                  onIncludePattern(log.patternHash as string);
                                }}
                              />
                            </Tooltip>
                          )}
                          {onExcludePattern && (
                            <Tooltip title="Hide logs like this (same message pattern)">
                              <Button
                                size="small"
                                className="!h-[18px] !w-[18px] !min-w-0 !p-0 text-[10px]"
                                icon={<MinusOutlined />}
                                aria-label="Hide logs like this"
                                onClick={(e) => {
                                  e.stopPropagation();
                                  onExcludePattern(log.patternHash as string);
                                }}
                              />
                            </Tooltip>
                          )}
                        </>
                      )}
                    </div>
                  </div>
                );
              })}
            </div>

            {isExecuting && queryResults.length > 0 && (
              <div className="flex justify-center py-2">
                <Spin indicator={<LoadingOutlined spin />} size="small" />
                <span className="ml-2 text-xs text-gray-500 dark:text-gray-300">
                  Loading more results...
                </span>
              </div>
            )}

            {!hasMoreResults && queryResults.length > 0 && (
              <div className="py-2 text-center text-xs text-gray-500 dark:text-gray-300">
                All {(totalResults < 0 ? queryResults.length : totalResults).toLocaleString()}{' '}
                results loaded
              </div>
            )}
          </div>
        )}
      </div>

      <LogDetailDrawerComponent
        log={selectedLog}
        onClose={() => setSelectedLog(null)}
        onAddFieldToQuery={onAddFieldToQuery}
        onExcludeFieldFromQuery={onExcludeFieldFromQuery}
        timezone={timezone}
        onOpenTrace={onOpenTrace}
        onShowContext={
          projectId
            ? (log) => {
                setSelectedLog(null);
                setContextLog(log);
              }
            : undefined
        }
      />

      {projectId && (
        <LogContextPanelComponent
          projectId={projectId}
          anchor={contextLog}
          timezone={timezone}
          onClose={() => setContextLog(null)}
          onOpenTrace={onOpenTrace}
          onOpenAsSearch={
            onOpenContextAsSearch
              ? (scope, from, to) => {
                  setContextLog(null);
                  onOpenContextAsSearch(scope, from, to);
                }
              : undefined
          }
        />
      )}
    </div>
  );
};
