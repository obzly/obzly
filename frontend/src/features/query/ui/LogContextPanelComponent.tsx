import {
  CloseOutlined,
  DownOutlined,
  FileSearchOutlined,
  FullscreenExitOutlined,
  FullscreenOutlined,
  PlusOutlined,
  SearchOutlined,
  UpOutlined,
} from '@ant-design/icons';
import { Alert, Button, Drawer, Dropdown, Input, Modal, Select, Spin, Tag, Tooltip } from 'antd';
import type { InputRef } from 'antd';
import dayjs from 'dayjs';
import React, { useEffect, useLayoutEffect, useMemo, useRef, useState } from 'react';

import {
  type LogContextResponse,
  type LogContextScope,
  type LogItem,
  queryApi,
} from '../../../entity/query';
import { getHttpStatus } from '../../../shared/api';
import { formatInTimezone, getUserTimeFormatWithMs } from '../../../shared/time';
import { levelColor } from '../lib/logLevels';

interface Props {
  projectId: string;
  /** The log to show context around; null closes the panel. */
  anchor: LogItem | null;
  timezone: string;
  onClose: () => void;
  /** Show a log's trace (Traces page). */
  onOpenTrace?: (traceId: string, timestamp: string) => void;
  /** Continue in the main search: these scope filters over [from, to]. */
  onOpenAsSearch?: (scope: LogContextScope[], from: dayjs.Dayjs, to: dayjs.Dayjs) => void;
}

type ViewMode = 'panel' | 'window';

const VIEW_MODE_STORAGE_KEY = 'obzly-log-context-view';
const WINDOW_OPTIONS = [
  { label: '±15 min', value: 15 },
  { label: '±1 hour', value: 60 },
  { label: '±6 hours', value: 360 },
  { label: '±24 hours', value: 1440 },
];
// The Site serves up to 1000 each side (logs_querier's maxContextRows).
const ROW_OPTIONS = [25, 50, 100, 200, 500, 1000].map((n) => ({
  label: `${n} each side`,
  value: n,
}));
// A pause this long between neighboring logs gets a "+3m 12s" divider.
const GAP_MARKER_MS = 60_000;
// Offered first in "+ field"; k8s_pod_name leads since one replica is the
// most common narrowing of the default workload scope.
const PREFERRED_FIELDS = ['k8s_pod_name', 'trace_id', 'service_name', 'host_name', 'k8s_node_name'];

const readViewMode = (): ViewMode => {
  try {
    return localStorage.getItem(VIEW_MODE_STORAGE_KEY) === 'window' ? 'window' : 'panel';
  } catch {
    return 'panel';
  }
};

const formatGap = (ms: number): string => {
  const seconds = Math.round(ms / 1000);
  if (seconds < 3600) return `+${Math.floor(seconds / 60)}m ${seconds % 60}s`;
  return `+${Math.floor(seconds / 3600)}h ${Math.floor((seconds % 3600) / 60)}m`;
};

const escapeRegExp = (value: string): string => value.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');

/** Case-insensitive matches of the find text, or null when there's none. */
const findPattern = (text: string): RegExp | null =>
  text.trim() ? new RegExp(escapeRegExp(text.trim()), 'gi') : null;

const countMatches = (message: string, pattern: RegExp): number =>
  message.match(pattern)?.length ?? 0;

/** The fields of the anchor a scope can be built from, preferred ones first. */
const scopeCandidates = (log: LogItem, applied: LogContextScope[]): string[] => {
  const names = new Set(Object.keys(log.fields ?? {}).filter((name) => log.fields?.[name]));
  if (log.traceId) names.add('trace_id');
  const taken = new Set(applied.map((s) => s.field));
  const rest = [...names].filter((n) => !taken.has(n) && !PREFERRED_FIELDS.includes(n)).sort();
  return [...PREFERRED_FIELDS.filter((n) => names.has(n) && !taken.has(n)), ...rest];
};

/**
 * The logs just before and after one log from the same source, scoped by
 * removable field chips, with paging, find, and an "Open as search" handoff.
 */
export const LogContextPanelComponent = ({
  projectId,
  anchor,
  timezone,
  onClose,
  onOpenTrace,
  onOpenAsSearch,
}: Props): React.JSX.Element => {
  const [viewMode, setViewMode] = useState<ViewMode>(readViewMode);
  // null = the default (source) scope; [] = the whole project.
  const [scopeFields, setScopeFields] = useState<string[] | null>(null);
  const [windowMinutes, setWindowMinutes] = useState(60);
  const [rows, setRows] = useState(50);
  const [data, setData] = useState<LogContextResponse | null>(null);
  const [isLoading, setIsLoading] = useState(false);
  const [loadingSide, setLoadingSide] = useState<'before' | 'after' | null>(null);
  const [error, setError] = useState<string | null>(null);
  // The Site predates log context (v0.52.0): its proxied route 404s.
  const [isUnsupported, setIsUnsupported] = useState(false);
  const [expandedId, setExpandedId] = useState<string | null>(null);
  const [isFindOpen, setIsFindOpen] = useState(false);
  const [findText, setFindText] = useState('');
  // The current match: its row and which match in the message it is (an
  // index would shift when "Load earlier" adds rows above).
  const [currentKey, setCurrentKey] = useState<{ id: string; nth: number } | null>(null);
  // Counts fresh contexts (not "Load earlier/later"), to restart Find.
  const [contextLoads, setContextLoads] = useState(0);

  const listRef = useRef<HTMLDivElement>(null);
  const findInputRef = useRef<InputRef>(null);
  const currentMarkRef = useRef<HTMLElement | null>(null);
  const anchorRef = useRef<HTMLDivElement>(null);
  const abortRef = useRef<AbortController | null>(null);
  // Prepending earlier rows keeps what's on screen where it was.
  const keepScrollRef = useRef<number | null>(null);
  const timeFormat = getUserTimeFormatWithMs();

  // A new anchor starts from the default scope.
  useEffect(() => {
    setScopeFields(null);
    setExpandedId(null);
  }, [anchor?.id]);

  useEffect(() => {
    if (!anchor) {
      setData(null);
      return;
    }
    abortRef.current?.abort();
    const abort = new AbortController();
    abortRef.current = abort;
    setIsLoading(true);
    setError(null);
    setIsUnsupported(false);
    queryApi
      .getLogContext(
        projectId,
        {
          anchor: { id: anchor.id, timestamp: anchor.timestamp },
          before: rows,
          after: rows,
          windowMinutes,
          ...(scopeFields ? { scope: scopeFields } : {}),
        },
        abort.signal,
      )
      .then((response) => {
        if (abort.signal.aborted) return;
        setData(response);
        setContextLoads((n) => n + 1);
      })
      .catch((e: unknown) => {
        if (abort.signal.aborted) return;
        if (getHttpStatus(e) === 404) setIsUnsupported(true);
        else setError((e as Error).message);
        setData(null);
      })
      .finally(() => {
        if (!abort.signal.aborted) setIsLoading(false);
      });
    return () => abort.abort();
  }, [projectId, anchor?.id, anchor?.timestamp, scopeFields, windowMinutes, rows]);

  // Center the anchor once a context arrives.
  useEffect(() => {
    // After a pop-out/pop-in the list is new, and a modal mounts a frame later.
    if (!data || isLoading) return;
    const frame = requestAnimationFrame(() =>
      anchorRef.current?.scrollIntoView({ block: 'center' }),
    );
    return () => cancelAnimationFrame(frame);
  }, [data?.anchor?.id, isLoading, viewMode]);

  useLayoutEffect(() => {
    const list = listRef.current;
    if (list && keepScrollRef.current !== null) {
      list.scrollTop += list.scrollHeight - keepScrollRef.current;
      keepScrollRef.current = null;
    }
  });

  const loadMore = (side: 'before' | 'after') => {
    if (!anchor || !data) return;
    const cursor = side === 'before' ? data.beforeCursor : data.afterCursor;
    if (!cursor) return;
    setLoadingSide(side);
    queryApi
      .getLogContext(projectId, {
        anchor: { id: anchor.id, timestamp: anchor.timestamp },
        [side]: rows,
        windowMinutes,
        scope: data.scope.map((s) => s.field),
        direction: side,
        cursor,
      })
      .then((more) => {
        if (side === 'before') {
          keepScrollRef.current = listRef.current?.scrollHeight ?? null;
          setData(
            (prev) =>
              prev && {
                ...prev,
                before: [...more.before, ...prev.before],
                beforeCursor: more.beforeCursor,
              },
          );
        } else {
          setData(
            (prev) =>
              prev && {
                ...prev,
                after: [...prev.after, ...more.after],
                afterCursor: more.afterCursor,
              },
          );
        }
      })
      .catch((e: unknown) => setError((e as Error).message))
      .finally(() => setLoadingSide(null));
  };

  const switchViewMode = (mode: ViewMode) => {
    setViewMode(mode);
    try {
      localStorage.setItem(VIEW_MODE_STORAGE_KEY, mode);
    } catch {
      /* storage unavailable - the choice just won't persist */
    }
  };

  const applied = data?.scope ?? [];
  const removeScope = (field: string) =>
    setScopeFields(applied.map((s) => s.field).filter((f) => f !== field));
  const addScope = (field: string) => setScopeFields([...applied.map((s) => s.field), field]);
  const candidates = data?.anchor ? scopeCandidates(data.anchor, applied) : [];

  const openAsSearch = () => {
    if (!anchor || !onOpenAsSearch) return;
    const at = dayjs(anchor.timestamp);
    onOpenAsSearch(applied, at.subtract(windowMinutes, 'minute'), at.add(windowMinutes, 'minute'));
  };

  const ordered = useMemo(
    () => (data?.anchor ? [...data.before, data.anchor, ...data.after] : []),
    [data],
  );
  const pattern = useMemo(
    () => (isFindOpen ? findPattern(findText) : null),
    [isFindOpen, findText],
  );
  // Each match as (row, nth match in its message), in list order.
  const matches = useMemo(() => {
    if (!pattern) return [];
    const found: { id: string; nth: number }[] = [];
    for (const log of ordered) {
      const count = countMatches(log.message ?? '', pattern);
      for (let nth = 0; nth < count; nth++) found.push({ id: log.id, nth });
    }
    return found;
  }, [ordered, pattern]);
  const currentIndex = Math.max(
    0,
    matches.findIndex((m) => m.id === currentKey?.id && m.nth === currentKey?.nth),
  );
  const current = matches.length > 0 ? matches[currentIndex] : null;

  // A new find text or fresh context starts at the first match at or after
  // the anchor, where the list opens.
  useEffect(() => {
    if (!data?.anchor || matches.length === 0) {
      setCurrentKey(null);
      return;
    }
    const anchorIndex = data.before.length;
    const rowIndex = new Map(ordered.map((log, i) => [log.id, i]));
    const first = matches.find((m) => (rowIndex.get(m.id) ?? 0) >= anchorIndex);
    setCurrentKey(first ?? matches[matches.length - 1]);
    // Not when "Load earlier/later" adds rows: that keeps the position.
  }, [findText, isFindOpen, contextLoads]);

  useEffect(() => {
    currentMarkRef.current?.scrollIntoView({ block: 'center' });
  }, [current?.id, current?.nth]);

  const stepMatch = (delta: number) => {
    if (matches.length === 0) return;
    setCurrentKey(matches[(currentIndex + delta + matches.length) % matches.length]);
  };

  const openFind = () => {
    setIsFindOpen(true);
    requestAnimationFrame(() => findInputRef.current?.focus({ cursor: 'all' }));
  };

  const closeFind = () => {
    setIsFindOpen(false);
    setFindText('');
  };

  // The message with the find text's matches marked; the current one stands
  // out and is what the list scrolls to.
  const renderMessage = (log: LogItem) => {
    const message = log.message ?? '';
    if (!pattern) return message;
    const parts: React.ReactNode[] = [];
    let last = 0;
    let nth = 0;
    for (const match of message.matchAll(pattern)) {
      const start = match.index ?? 0;
      if (start > last) parts.push(message.slice(last, start));
      const isCurrent = current?.id === log.id && current.nth === nth;
      parts.push(
        <mark
          key={start}
          ref={
            isCurrent
              ? (el) => {
                  currentMarkRef.current = el;
                }
              : undefined
          }
          className={`rounded-sm px-0 ${
            isCurrent
              ? 'bg-orange-400 text-gray-900 ring-1 ring-orange-600'
              : 'bg-yellow-200 text-inherit dark:bg-yellow-600/50'
          }`}
        >
          {match[0]}
        </mark>,
      );
      last = start + match[0].length;
      nth++;
    }
    if (last < message.length) parts.push(message.slice(last));
    return parts;
  };

  const renderRow = (log: LogItem, isAnchor: boolean) => {
    const level = (log.level || 'unknown').toLowerCase();
    const isProblem = level === 'error' || level === 'fatal' || level === 'warn';
    const expanded = expandedId === log.id;
    // The row holding the current match shows its whole message.
    const wrapped = expanded || current?.id === log.id;
    return (
      <div
        key={log.id}
        ref={isAnchor ? anchorRef : undefined}
        className={`cursor-pointer border-l-[3px] px-2 py-0.5 font-mono text-xs ${
          isAnchor
            ? 'sticky top-0 bottom-0 z-10 bg-emerald-50 ring-1 ring-emerald-400 dark:bg-emerald-950'
            : isProblem
              ? 'bg-red-50/60 hover:bg-red-50 dark:bg-red-950/30 dark:hover:bg-red-950/50'
              : 'hover:bg-gray-100 dark:hover:bg-gray-700'
        }`}
        style={{ borderLeftColor: levelColor(level) }}
        onClick={() => setExpandedId(expanded ? null : log.id)}
      >
        <div className="flex gap-3">
          <span className="shrink-0 text-gray-500 dark:text-gray-400">
            {formatInTimezone(log.timestamp, timeFormat.format, timezone)}
          </span>
          <span className="w-12 shrink-0 font-semibold" style={{ color: levelColor(level) }}>
            {level}
          </span>
          <span
            className={`min-w-0 flex-1 text-gray-800 dark:text-gray-100 ${wrapped ? 'break-all whitespace-pre-wrap' : 'truncate'}`}
          >
            {renderMessage(log)}
          </span>
        </div>
        {expanded && (
          <div className="mt-1 mb-1 ml-2 grid grid-cols-[minmax(8rem,auto)_1fr] gap-x-3 text-[11px]">
            {log.traceId && (
              <>
                <span className="text-gray-500">trace_id</span>
                <span className="break-all">
                  {log.traceId}
                  {onOpenTrace && (
                    <Button
                      type="link"
                      size="small"
                      className="!h-auto !py-0"
                      onClick={(e) => {
                        e.stopPropagation();
                        onOpenTrace(log.traceId as string, log.timestamp);
                      }}
                    >
                      Open trace
                    </Button>
                  )}
                </span>
              </>
            )}
            {Object.entries(log.fields ?? {}).map(([key, value]) => (
              <React.Fragment key={key}>
                <span className="text-gray-500">{key}</span>
                <span className="break-all">{String(value)}</span>
              </React.Fragment>
            ))}
          </div>
        )}
      </div>
    );
  };

  const renderList = () => {
    if (!data?.anchor) return null;
    const anchorIndex = data.before.length;
    return ordered.map((log, index) => {
      const previous = ordered[index - 1];
      const gap = previous
        ? new Date(log.timestamp).valueOf() - new Date(previous.timestamp).valueOf()
        : 0;
      return (
        <React.Fragment key={log.id}>
          {gap >= GAP_MARKER_MS && (
            <div className="my-0.5 flex items-center gap-2 px-2 text-[10px] text-gray-400">
              <div className="h-px flex-1 bg-gray-200 dark:bg-gray-700" />
              {formatGap(gap)}
              <div className="h-px flex-1 bg-gray-200 dark:bg-gray-700" />
            </div>
          )}
          {renderRow(log, index === anchorIndex)}
        </React.Fragment>
      );
    });
  };

  const header = (
    <div className="flex flex-wrap items-center gap-2">
      <span className="text-xs text-gray-500 dark:text-gray-400">Scope</span>
      {applied.length === 0 && data?.anchor && <Tag>All project logs</Tag>}
      {applied.map((s) => (
        <Tag key={s.field} closable onClose={() => removeScope(s.field)} className="font-mono">
          {s.field} = {s.value}
        </Tag>
      ))}
      <Dropdown
        trigger={['click']}
        menu={{
          items: [
            ...candidates.slice(0, 30).map((field) => ({ key: field, label: field })),
            ...(candidates.length > 0 ? [{ type: 'divider' as const }] : []),
            { key: '__source', label: 'Same workload (default)' },
            { key: '__all', label: 'All project logs' },
          ],
          onClick: ({ key }) => {
            if (key === '__source') setScopeFields(null);
            else if (key === '__all') setScopeFields([]);
            else addScope(key);
          },
        }}
      >
        <Button size="small" icon={<PlusOutlined />}>
          Field
        </Button>
      </Dropdown>
      <Select
        size="small"
        value={windowMinutes}
        options={WINDOW_OPTIONS}
        onChange={setWindowMinutes}
      />
      <Select size="small" value={rows} options={ROW_OPTIONS} onChange={setRows} />
      <Tooltip title={isFindOpen ? 'Turn off Find' : 'Find text in these logs and highlight it'}>
        <Button
          size="small"
          icon={<FileSearchOutlined />}
          onClick={isFindOpen ? closeFind : openFind}
          disabled={!data?.anchor}
          type={isFindOpen ? 'primary' : 'default'}
        >
          Find
        </Button>
      </Tooltip>
      {onOpenAsSearch && (
        <Tooltip title="Search these logs on the main page (scope filters over the context window)">
          <Button
            size="small"
            icon={<SearchOutlined />}
            onClick={openAsSearch}
            disabled={!data?.anchor}
          >
            Open as search
          </Button>
        </Tooltip>
      )}
    </div>
  );

  const body = (
    <div className="flex h-full flex-col gap-2">
      {header}
      {isFindOpen && (
        <div className="flex items-center gap-1">
          <Input
            ref={findInputRef}
            size="small"
            allowClear
            className="max-w-sm"
            prefix={<FileSearchOutlined className="text-gray-400" />}
            placeholder="Find in messages"
            value={findText}
            onChange={(e) => setFindText(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === 'Enter') stepMatch(e.shiftKey ? -1 : 1);
              if (e.key === 'Escape') {
                e.stopPropagation();
                closeFind();
              }
            }}
          />
          <span className="min-w-16 text-center text-xs text-gray-500 tabular-nums dark:text-gray-400">
            {findText.trim()
              ? matches.length > 0
                ? `${currentIndex + 1} of ${matches.length}`
                : 'No matches'
              : ''}
          </span>
          <Tooltip title="Previous match (Shift+Enter)">
            <Button
              size="small"
              type="text"
              icon={<UpOutlined />}
              disabled={matches.length === 0}
              onClick={() => stepMatch(-1)}
              aria-label="Previous match"
            />
          </Tooltip>
          <Tooltip title="Next match (Enter)">
            <Button
              size="small"
              type="text"
              icon={<DownOutlined />}
              disabled={matches.length === 0}
              onClick={() => stepMatch(1)}
              aria-label="Next match"
            />
          </Tooltip>
          <Button
            size="small"
            type="text"
            icon={<CloseOutlined />}
            onClick={closeFind}
            aria-label="Close find"
          />
        </div>
      )}
      {isUnsupported && (
        <Alert
          type="info"
          showIcon
          message="This Site doesn't support logs around a result yet"
          description="Upgrade the Site to Obzly v0.52.0 or later."
        />
      )}
      {error && <Alert type="error" showIcon message={error} />}
      {data?.partial && (
        <Alert
          type="warning"
          showIcon
          message="The search ran out of time - some logs may be missing."
        />
      )}
      {!isLoading && data && !data.anchor && (
        <Alert
          type="info"
          showIcon
          message="This log is no longer stored (deleted or past retention)."
        />
      )}
      <div
        ref={listRef}
        className="min-h-0 flex-1 overflow-auto rounded border border-gray-200 dark:border-gray-700"
      >
        {isLoading && (
          <div className="flex justify-center py-6">
            <Spin size="small" />
          </div>
        )}
        {!isLoading && data?.anchor && (
          <>
            <div className="flex justify-center py-1">
              {data.beforeCursor ? (
                <Button
                  size="small"
                  type="link"
                  loading={loadingSide === 'before'}
                  onClick={() => loadMore('before')}
                >
                  Load earlier
                </Button>
              ) : (
                <span className="text-[10px] text-gray-400">
                  No earlier logs within ±
                  {windowMinutes >= 60 ? `${windowMinutes / 60}h` : `${windowMinutes}m`}
                </span>
              )}
            </div>
            {renderList()}
            <div className="flex justify-center py-1">
              {data.afterCursor ? (
                <Button
                  size="small"
                  type="link"
                  loading={loadingSide === 'after'}
                  onClick={() => loadMore('after')}
                >
                  Load later
                </Button>
              ) : (
                <span className="text-[10px] text-gray-400">No later logs in the window</span>
              )}
            </div>
          </>
        )}
      </div>
    </div>
  );

  const title = anchor
    ? `Logs around ${formatInTimezone(anchor.timestamp, timeFormat.format, timezone)}`
    : 'Logs around';
  const toggle =
    viewMode === 'panel' ? (
      <Tooltip title="Open in a large window">
        <Button
          type="text"
          icon={<FullscreenOutlined />}
          onClick={() => switchViewMode('window')}
        />
      </Tooltip>
    ) : (
      <Tooltip title="Back to the side panel">
        <Button
          type="text"
          icon={<FullscreenExitOutlined />}
          onClick={() => switchViewMode('panel')}
        />
      </Tooltip>
    );

  if (viewMode === 'window') {
    return (
      <Modal
        open={!!anchor}
        onCancel={onClose}
        footer={null}
        closable={false}
        width="95vw"
        centered
        title={
          <div className="flex items-center justify-between">
            <span>{title}</span>
            <span>
              {toggle}
              <Button type="text" icon={<CloseOutlined />} onClick={onClose} />
            </span>
          </div>
        }
        styles={{ body: { height: '82vh' } }}
      >
        {body}
      </Modal>
    );
  }

  return (
    <Drawer
      title={title}
      open={!!anchor}
      onClose={onClose}
      size={Math.round(window.innerWidth * 0.6)}
      closable={false}
      extra={
        <span>
          {toggle}
          <Button type="text" icon={<CloseOutlined />} onClick={onClose} />
        </span>
      }
      styles={{ body: { display: 'flex', flexDirection: 'column', paddingTop: 12 } }}
    >
      {body}
    </Drawer>
  );
};
