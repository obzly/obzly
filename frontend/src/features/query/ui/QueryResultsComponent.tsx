import {
  CheckOutlined,
  ColumnHeightOutlined,
  CopyOutlined,
  DownloadOutlined,
  LoadingOutlined,
  MenuUnfoldOutlined,
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
import {
  type DetailPlacement,
  type DetailWidth,
  LAYOUT_PRESETS,
  type LogsViewSettings,
  type RowDensity,
  type TextSize,
  activePreset,
} from '../lib/logsViewSettings';
import { LevelPillComponent } from './LevelPillComponent';
import { LogContextPanelComponent } from './LogContextPanelComponent';
import { type DetailViewMode, LogDetailPanelComponent } from './LogDetailPanelComponent';
import { ToggleGroupComponent } from './ToggleGroupComponent';

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
// The side details panel's S/M/L widths; it never takes more than DETAIL_MAX_SHARE.
const DETAIL_WIDTHS: Record<DetailWidth, number> = { s: 340, m: 440, l: 600 };
const DETAIL_MAX_SHARE = '45%';
// The bottom details panel takes at most this share of the page height.
const DETAIL_BELOW_SHARE = 0.45;

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
  /** The page layout (presets, details placement, rows) and its setters. */
  view: LogsViewSettings;
  onViewChange: (patch: Partial<LogsViewSettings>) => void;
  onViewReset: () => void;
  /** The Newest/Oldest sort button, shown in the toolbar. */
  sortControl?: React.ReactNode;
  /** Live tail controls (Live, Pause/Resume, Clear), shown in the toolbar. */
  liveControls?: React.ReactNode;
  /** The Lines / Chart switch, first in the toolbar. */
  viewSwitch?: React.ReactNode;
  /** Set while the fields panel is hidden: shows a button that brings it back. */
  onShowFields?: () => void;
  /** Height of the page's scroll area, so the details panel fits on screen. */
  detailMaxHeight?: number;
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
  view,
  onViewChange,
  onViewReset,
  sortControl,
  liveControls,
  viewSwitch,
  onShowFields,
  detailMaxHeight,
}: Props): React.JSX.Element => {
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
  // Kept here so it survives switching lines (and inline rows scrolling away).
  const [detailViewMode, setDetailViewMode] = useState<DetailViewMode>('json');
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
    // Hidden behind another Logs view or the Chart view the page is short,
    // so "near the bottom" is always true and pages would load forever.
    if (!containerRef.current || containerRef.current.getClientRects().length === 0) {
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
    // Also re-checks loading when the list is shown again (it had no size).
    const observer = new ResizeObserver(() => {
      updateScrollMargin();
      handleScroll();
    });
    for (const child of Array.from(scroller.children)) observer.observe(child);
    if (containerRef.current) observer.observe(containerRef.current);
    return () => observer.disconnect();
  }, [getScrollElement, updateScrollMargin, handleScroll, hasExecuted]);

  // Rows are measured dynamically since wrapped rows vary in height.
  // eslint-disable-next-line react-hooks/incompatible-library
  const rowVirtualizer = useVirtualizer({
    count: queryResults.length,
    getScrollElement,
    estimateSize: () => Math.round(view.textSize * 1.5) + (view.density === 'compact' ? 4 : 8),
    overscan: 10,
    scrollMargin,
  });

  const isInlineDetail = view.detail === 'inline';
  const hasSidePanel = !!selectedLog && view.detail === 'side';

  // Wrap, columns, row size and the side panel change row heights - drop
  // cached measurements.
  useEffect(() => {
    rowVirtualizer.measure();
  }, [
    wrap,
    visibleColumns,
    view.density,
    view.textSize,
    view.detail,
    view.detailWidth,
    hasSidePanel,
    rowVirtualizer,
  ]);

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

  // Esc closes the details (the drawer they replace did too); the logs-around
  // panel handles its own Esc while it's open.
  useEffect(() => {
    if (!selectedLog || contextLog) return;
    const onKeyDown = (e: KeyboardEvent) => {
      if (e.key !== 'Escape' || isTypingTarget(e.target)) return;
      setSelectedLog(null);
    };
    document.addEventListener('keydown', onKeyDown);
    return () => document.removeEventListener('keydown', onKeyDown);
  }, [selectedLog, contextLog]);

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

  // Clicking the open line again closes its details.
  const toggleSelected = (log: LogItem) =>
    setSelectedLog((prev) => (prev?.id === log.id ? null : log));

  const showContext = projectId
    ? (log: LogItem) => {
        setSelectedLog(null);
        setContextLog(log);
      }
    : undefined;

  const widthOf = (id: ColumnId) => columnWidths[id] ?? DEFAULT_FIELD_COLUMN_WIDTH;

  const renderCell = (id: ColumnId, log: LogItem): React.ReactNode => {
    switch (id) {
      case 'timestamp':
        return (
          <Tooltip title={dayjs(log.timestamp).fromNow()}>
            <span className="whitespace-nowrap text-gray-500 dark:text-gray-400">
              {formatInTimezone(log.timestamp, timeFormat.format, timezone)}
            </span>
          </Tooltip>
        );
      case 'level':
        return <LevelPillComponent level={log.level} />;
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
              <span className="text-accent-600 hover:text-accent-700 dark:text-accent-400 ml-1">
                (view details)
              </span>
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
      <div className="mb-1 text-xs font-semibold tracking-wide text-gray-500 uppercase dark:text-gray-400">
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
      <div className="mt-2 mb-1 border-t border-gray-100 pt-2 text-xs font-semibold tracking-wide text-gray-500 uppercase dark:border-gray-700 dark:text-gray-400">
        Fields
      </div>
      <Input
        size="small"
        allowClear
        placeholder="Search fields"
        value={columnSearch}
        onChange={(e) => setColumnSearch(e.target.value)}
      />
      <div className="mt-1 flex max-h-56 flex-col gap-0.5 overflow-auto">
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
        className="mt-2"
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

  const preset = activePreset(view);
  const presetLabel = LAYOUT_PRESETS.find((p) => p.id === preset)?.label;

  const sectionLabel = (text: string, divided = false) => (
    <span
      className={`col-span-2 text-[11px] font-semibold tracking-wider text-gray-500 uppercase dark:text-gray-400 ${
        divided ? 'mt-1 border-t border-gray-100 pt-2 dark:border-gray-700' : ''
      }`}
    >
      {text}
    </span>
  );

  const viewSettings = (
    <div className="grid w-[340px] max-w-[calc(100vw-48px)] grid-cols-[104px_minmax(0,1fr)] items-center gap-x-3 gap-y-2 text-[13px]">
      {sectionLabel('Display')}
      <span className="text-gray-500 dark:text-gray-400">Timezone</span>
      <Select
        size="small"
        showSearch
        value={timezone}
        onChange={handleTimezoneChange}
        options={timezoneOptions}
        className="w-full min-w-0"
        popupMatchSelectWidth={false}
        aria-label="Timezone"
        filterOption={(input, option) =>
          (option?.label ?? '').toLowerCase().includes(input.toLowerCase())
        }
      />
      <label htmlFor="messageLength" className="text-gray-500 dark:text-gray-400">
        Message length
      </label>
      <span
        className="flex items-center gap-2"
        title="Characters shown per message while Wrap is off"
      >
        <input
          id="messageLength"
          type="number"
          value={messageLength}
          onChange={(e) => {
            const parsed = parseInt(e.target.value, 10);
            if (!isNaN(parsed)) setMessageLength(Math.min(1000, Math.max(1, parsed)));
          }}
          className="focus:border-accent-600 focus:ring-accent-600 w-20 rounded-md border border-gray-300 bg-white px-1.5 py-0.5 text-xs text-gray-900 focus:ring-1 focus:outline-none dark:border-gray-600 dark:bg-gray-900 dark:text-gray-100"
          min="1"
          max="1000"
        />
        <span className="text-[11px] text-gray-400">
          {wrap ? 'ignored while Wrap is on' : 'characters'}
        </span>
      </span>

      {sectionLabel('Layout', true)}
      <span className="text-gray-500 dark:text-gray-400">Panels</span>
      <span>
        <ToggleGroupComponent<'fields' | 'chart'>
          label="Panels"
          size="small"
          options={[
            { value: 'fields', label: 'Fields', pressed: view.showFields },
            { value: 'chart', label: 'Chart', pressed: view.showChart },
          ]}
          onToggle={(panel) =>
            onViewChange(
              panel === 'fields'
                ? { showFields: !view.showFields }
                : { showChart: !view.showChart },
            )
          }
        />
      </span>
      <span className="text-gray-500 dark:text-gray-400">Details</span>
      <span>
        <ToggleGroupComponent<DetailPlacement>
          label="Details"
          size="small"
          options={(
            [
              ['side', 'Side', 'Beside the lines'],
              ['below', 'Below', 'Docked under the lines'],
              ['inline', 'In row', 'Inside the clicked row'],
            ] as const
          ).map(([value, label, hint]) => ({
            value,
            label,
            hint,
            pressed: view.detail === value,
          }))}
          onToggle={(detail) => onViewChange({ detail })}
        />
      </span>
      <span className="text-gray-500 dark:text-gray-400">Details width</span>
      <span>
        <ToggleGroupComponent<DetailWidth>
          label="Details width"
          size="small"
          options={(['s', 'm', 'l'] as const).map((value) => ({
            value,
            label: value.toUpperCase(),
            pressed: view.detailWidth === value,
          }))}
          // A width only applies beside the lines, so picking one moves them there.
          onToggle={(detailWidth) => onViewChange({ detailWidth, detail: 'side' })}
        />
      </span>
      <span className="text-gray-500 dark:text-gray-400">Rows</span>
      <span>
        <ToggleGroupComponent<RowDensity>
          label="Rows"
          size="small"
          options={(
            [
              ['comfortable', 'Comfortable'],
              ['compact', 'Compact'],
            ] as const
          ).map(([value, label]) => ({ value, label, pressed: view.density === value }))}
          onToggle={(density) => onViewChange({ density })}
        />
      </span>
      <span className="text-gray-500 dark:text-gray-400">Text size</span>
      <span>
        <ToggleGroupComponent<TextSize>
          label="Text size"
          size="small"
          options={([12, 13, 14] as const).map((value) => ({
            value,
            label: String(value),
            pressed: view.textSize === value,
          }))}
          onToggle={(textSize) => onViewChange({ textSize })}
        />
      </span>
      <span className="col-span-2 mt-1 flex items-center justify-between gap-2 border-t border-gray-100 pt-2 dark:border-gray-700">
        <span className="text-[11px] text-gray-400">Saved for you in this browser</span>
        <Tooltip title="Back to the Split layout and the default message length">
          <Button
            size="small"
            type="link"
            className="!px-0"
            onClick={() => {
              onViewReset();
              setMessageLength(1000);
            }}
          >
            Reset
          </Button>
        </Tooltip>
      </span>
    </div>
  );

  const countText =
    isExecuting && queryResults.length === 0
      ? 'Searching…'
      : `${queryResults.length.toLocaleString()}${totalResults > queryResults.length ? `+ of ${totalResults.toLocaleString()}` : totalResults < 0 && hasMoreResults ? '+' : ''} results${queryResults.length > 0 ? ' loaded' : ' found'}`;

  const rowPadding =
    view.density === 'compact' ? (wrap ? 'py-1' : 'py-0.5') : wrap ? 'py-1.5' : 'py-1';
  const rowActionButton = '!h-5 !w-5 !min-w-0 !p-0 text-[11px]';

  const detailPanel = (variant: 'side' | 'below' | 'inline', log: LogItem) => (
    <LogDetailPanelComponent
      log={log}
      variant={variant}
      viewMode={detailViewMode}
      onViewModeChange={setDetailViewMode}
      onClose={() => setSelectedLog(null)}
      onAddFieldToQuery={onAddFieldToQuery}
      onExcludeFieldFromQuery={onExcludeFieldFromQuery}
      timezone={timezone}
      onOpenTrace={onOpenTrace}
      onShowContext={showContext}
      onIncludePattern={onIncludePattern}
    />
  );

  const resultsCard = (
    <section
      ref={containerRef}
      aria-label="Log lines"
      className="w-full min-w-0 flex-1 rounded-2xl border border-gray-200 bg-white dark:border-gray-700 dark:bg-gray-800"
    >
      <div className="flex flex-wrap items-center justify-between gap-3 border-b border-gray-200 px-4 py-3 dark:border-gray-700">
        <div className="flex min-w-0 items-center gap-3">
          {onShowFields && (
            <Tooltip title="Show the fields panel">
              <Button
                size="small"
                icon={<MenuUnfoldOutlined />}
                onClick={onShowFields}
                aria-label="Show fields panel"
              >
                Fields
              </Button>
            </Tooltip>
          )}
          <div className="min-w-0">
            <h2 className="m-0 text-[15px] font-semibold text-gray-900 dark:text-gray-50">Lines</h2>
            <p className="m-0 mt-0.5 flex items-center gap-1 text-xs text-gray-500 dark:text-gray-400">
              {isExecuting && queryResults.length === 0 && (
                <Spin indicator={<LoadingOutlined spin />} size="small" />
              )}
              <span>
                {countText} · {presetLabel ? `${presetLabel} layout` : 'Custom layout'}
                {!selectedLog && queryResults.length > 0 && ' · click a line for its details'}
              </span>
            </p>
          </div>
        </div>
        <div className="flex flex-wrap items-center gap-2">
          {viewSwitch}
          {liveControls}
          <ToggleGroupComponent
            label="Layout"
            options={LAYOUT_PRESETS.map((p) => ({
              value: p.id,
              label: p.label,
              hint: p.hint,
              pressed: preset === p.id,
            }))}
            onToggle={(id) => {
              const next = LAYOUT_PRESETS.find((p) => p.id === id);
              if (next) onViewChange(next.patch);
            }}
          />
          {sortControl}
          <Tooltip title="Wrap long messages (W)">
            <label className="flex min-h-8 cursor-pointer items-center gap-1.5 rounded-[10px] border border-gray-200 px-2.5 text-xs text-gray-500 dark:border-gray-700 dark:text-gray-300">
              Wrap
              <kbd className="rounded border border-gray-300 px-1 font-mono text-[10px] leading-4 dark:border-gray-600">
                W
              </kbd>
              <Switch size="small" checked={wrap} onChange={setWrap} />
            </label>
          </Tooltip>

          <Popover content={columnPicker} trigger="click" placement="bottomRight" destroyOnHidden>
            <Tooltip title="Choose columns">
              <Button icon={<TableOutlined />} aria-label="Choose columns" />
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
              <Button icon={<DownloadOutlined />} aria-label="Export results" />
            </Tooltip>
          </Dropdown>

          <Popover content={viewSettings} trigger="click" placement="bottomRight" destroyOnHidden>
            <Tooltip title="View settings (timezone, message length, layout)">
              <Button icon={<SettingOutlined />} aria-label="View settings" />
            </Tooltip>
          </Popover>
        </div>
      </div>

      <div className="px-2 pt-1 pb-2">
        {!hasExecuted && !isExecuting ? (
          <div className="flex h-20 items-center justify-center text-sm text-gray-500 dark:text-gray-400">
            Run a search to see matching logs.
          </div>
        ) : isExecuting && queryResults.length === 0 ? (
          <div className="flex h-32 items-center justify-center text-gray-600 dark:text-gray-300">
            <Spin indicator={<LoadingOutlined spin />} />
            <span className="ml-2 text-sm">Executing query...</span>
          </div>
        ) : queryResults.length === 0 ? (
          <div className="flex h-20 items-center justify-center text-sm text-gray-500 dark:text-gray-400">
            No logs found matching your query.
          </div>
        ) : (
          <div className="space-y-1">
            <div className="sticky top-0 z-10 flex gap-3 border-b border-l-[3px] border-gray-200 border-l-transparent bg-white pt-2 pr-[96px] pb-1.5 pl-2 text-xs font-medium text-gray-500 dark:border-gray-700 dark:border-l-transparent dark:bg-gray-800 dark:text-gray-400">
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
                      className="hover:bg-accent-400 absolute top-0 right-0 z-10 h-full w-1.5 cursor-col-resize"
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
                    tabIndex={0}
                    aria-current={isSelected ? 'true' : undefined}
                    style={{
                      position: 'absolute',
                      top: 0,
                      left: 0,
                      width: '100%',
                      // virtualRow.start includes scrollMargin; rows sit in the list.
                      transform: `translateY(${virtualRow.start - scrollMargin}px)`,
                      borderLeftColor: levelColor(log.level),
                      fontSize: view.textSize,
                    }}
                    className={`group cursor-pointer rounded-r-md border-l-[3px] pr-[96px] pl-2 ${rowPadding} focus-visible:ring-accent-500 !font-mono outline-none hover:bg-gray-100 focus-visible:ring-2 focus-visible:ring-inset dark:hover:bg-gray-700/60 ${
                      isSelected
                        ? 'bg-accent-50 dark:bg-accent-900/30'
                        : virtualRow.index % 2 === 1
                          ? 'bg-gray-50/70 dark:bg-gray-900/40'
                          : ''
                    }`}
                    onClick={() => toggleSelected(log)}
                    onKeyDown={(e) => {
                      if (e.target !== e.currentTarget) return;
                      if (e.key === 'Enter' || e.key === ' ') {
                        e.preventDefault();
                        toggleSelected(log);
                      }
                    }}
                    onMouseEnter={() => setHoveredTimestamp(log.timestamp)}
                  >
                    <div className="flex items-start gap-3">
                      {shownColumns.map((id) => (
                        <div key={id} className={cellClass(id)} style={cellStyle(id)}>
                          {renderCell(id, log)}
                        </div>
                      ))}
                    </div>
                    {/* In the reserved right gutter (pr-[96px]) so they never cover the message;
                        shown on hover, on keyboard focus and on the open line. */}
                    <div
                      className={`absolute top-0 right-1 items-center gap-0.5 pt-0.5 ${
                        isSelected ? 'flex' : 'hidden group-focus-within:flex group-hover:flex'
                      }`}
                    >
                      <Tooltip title={copiedLogId === log.id ? 'Copied' : 'Copy message'}>
                        <Button
                          size="small"
                          className={rowActionButton}
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
                            className={rowActionButton}
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
                                className={rowActionButton}
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
                                className={rowActionButton}
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
                    {isSelected && isInlineDetail && detailPanel('inline', log)}
                  </div>
                );
              })}
            </div>

            {isExecuting && queryResults.length > 0 && (
              <div className="flex justify-center py-2">
                <Spin indicator={<LoadingOutlined spin />} size="small" />
                <span className="ml-2 text-xs text-gray-500 dark:text-gray-400">
                  Loading more results...
                </span>
              </div>
            )}

            {!hasMoreResults && queryResults.length > 0 && (
              <div className="py-2 text-center text-xs text-gray-500 dark:text-gray-400">
                All {(totalResults < 0 ? queryResults.length : totalResults).toLocaleString()}{' '}
                results loaded
              </div>
            )}
          </div>
        )}
      </div>
    </section>
  );

  return (
    <>
      <div className={view.detail === 'below' ? 'flex flex-col gap-4' : 'flex items-start gap-4'}>
        {resultsCard}
        {/* Beside the lines: stays in view while the page scrolls. */}
        {selectedLog && view.detail === 'side' && (
          <div
            className="sticky top-0 shrink-0 self-start overflow-auto rounded-2xl"
            style={{
              width: `min(${DETAIL_WIDTHS[view.detailWidth]}px, ${DETAIL_MAX_SHARE})`,
              maxHeight: detailMaxHeight,
            }}
          >
            {detailPanel('side', selectedLog)}
          </div>
        )}
        {/* Under the lines: docked to the bottom of the view, since the list
            below it loads more as it scrolls and has no end to reach. */}
        {selectedLog && view.detail === 'below' && (
          <div
            className="sticky bottom-0 z-20 overflow-auto rounded-2xl"
            style={{
              maxHeight: detailMaxHeight
                ? Math.round(detailMaxHeight * DETAIL_BELOW_SHARE)
                : '45vh',
            }}
          >
            {detailPanel('below', selectedLog)}
          </div>
        )}
      </div>

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
    </>
  );
};
