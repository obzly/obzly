import {
  ApartmentOutlined,
  CloseOutlined,
  ColumnHeightOutlined,
  CopyOutlined,
} from '@ant-design/icons';
import { Button, Drawer, Segmented, Table, Tag } from 'antd';
import React, { useEffect, useRef, useState } from 'react';
import { Prism as SyntaxHighlighter } from 'react-syntax-highlighter';
import { tomorrow } from 'react-syntax-highlighter/dist/esm/styles/prism';

import type { LogItem } from '../../../entity/query';
import { copyToClipboard } from '../../../shared/lib';
import { LOCAL_TIMEZONE, formatInTimezone, getUserTimeFormatWithMs } from '../../../shared/time';

interface Props {
  log: LogItem | null;
  onClose: () => void;
  onAddFieldToQuery?: (fieldName: string, fieldValue: string) => void;
  /** Display timezone, same selector as QueryResultsComponent's header. */
  timezone?: string;
  /** "−" next to a field value: hide logs with that value. */
  onExcludeFieldFromQuery?: (fieldName: string, fieldValue: string) => void;
  /** "Show logs around" - opens the context panel for this log. */
  onShowContext?: (log: LogItem) => void;
  /** "Open trace" - shows the trace this log belongs to (Traces page). */
  onOpenTrace?: (traceId: string, timestamp: string) => void;
}

type ViewMode = 'json' | 'table';

const WIDTH_STORAGE_KEY = 'obzly-log-detail-drawer-width';
const DEFAULT_WIDTH = 640;
const MIN_WIDTH = 360;
// Leave some of the results table visible behind the drawer.
const maxWidth = () => Math.max(MIN_WIDTH, window.innerWidth - 80);
const clampWidth = (width: number) => Math.min(Math.max(width, MIN_WIDTH), maxWidth());

const readStoredWidth = (): number => {
  try {
    const stored = Number(localStorage.getItem(WIDTH_STORAGE_KEY));
    return Number.isFinite(stored) && stored > 0 ? clampWidth(stored) : DEFAULT_WIDTH;
  } catch {
    return DEFAULT_WIDTH;
  }
};

interface DetailRow {
  key: string;
  value: string;
  isCustomField: boolean;
}

/** A log's full record as raw JSON or a click-to-filter table, in a resizable drawer. */
export const LogDetailDrawerComponent = ({
  log,
  onClose,
  onAddFieldToQuery,
  timezone = LOCAL_TIMEZONE,
  onExcludeFieldFromQuery,
  onShowContext,
  onOpenTrace,
}: Props): React.JSX.Element => {
  const [viewMode, setViewMode] = useState<ViewMode>('json');
  const [isCopying, setIsCopying] = useState(false);
  // The dragged width is remembered and re-clamped if the window shrank since.
  const [width, setWidth] = useState<number>(readStoredWidth);
  const widthRef = useRef(width);
  useEffect(() => {
    widthRef.current = width;
  }, [width]);
  useEffect(() => {
    const onWindowResize = () => setWidth((w) => clampWidth(w));
    window.addEventListener('resize', onWindowResize);
    return () => window.removeEventListener('resize', onWindowResize);
  }, []);

  const timeFormat = getUserTimeFormatWithMs();
  const formattedTimestamp = log
    ? formatInTimezone(log.timestamp, timeFormat.format, timezone)
    : '';

  const jsonText = log
    ? JSON.stringify(
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
      )
    : '';

  const handleCopy = async () => {
    setIsCopying(true);
    try {
      await copyToClipboard(jsonText);
    } finally {
      setTimeout(() => setIsCopying(false), 300);
    }
  };

  const rows: DetailRow[] = log
    ? [
        {
          key: 'timestamp',
          value: formattedTimestamp,
          isCustomField: false,
        },
        { key: 'level', value: log.level, isCustomField: false },
        { key: 'message', value: log.message, isCustomField: false },
        ...(log.clientIp ? [{ key: 'client_ip', value: log.clientIp, isCustomField: false }] : []),
        // Filterable like a field (trace_id:<id> finds the trace's other logs).
        ...(log.traceId ? [{ key: 'trace_id', value: log.traceId, isCustomField: true }] : []),
        ...(log.spanId ? [{ key: 'span_id', value: log.spanId, isCustomField: true }] : []),
        ...Object.entries(log.fields ?? {}).map(([key, value]) => ({
          key,
          value,
          isCustomField: true,
        })),
      ]
    : [];

  return (
    <Drawer
      title={
        <div className="flex items-center gap-3">
          <Segmented
            size="small"
            value={viewMode}
            onChange={(v) => setViewMode(v as ViewMode)}
            options={[
              { label: 'JSON', value: 'json' },
              { label: 'Table', value: 'table' },
            ]}
          />
          {log && <span>{`Log at ${formattedTimestamp}`}</span>}
        </div>
      }
      open={!!log}
      onClose={onClose}
      size={width}
      maxSize={maxWidth()}
      resizable={{
        onResize: (size) => setWidth(clampWidth(size)),
        onResizeEnd: () => {
          try {
            localStorage.setItem(WIDTH_STORAGE_KEY, String(Math.round(widthRef.current)));
          } catch {
            /* storage unavailable - the width just won't persist */
          }
        },
      }}
      closable={false}
      extra={
        <span className="flex items-center gap-1">
          {log?.traceId && onOpenTrace && (
            <Button
              size="small"
              icon={<ApartmentOutlined />}
              onClick={() => onOpenTrace(log.traceId as string, log.timestamp)}
            >
              Open trace
            </Button>
          )}
          {log && onShowContext && (
            <Button size="small" icon={<ColumnHeightOutlined />} onClick={() => onShowContext(log)}>
              Show logs around
            </Button>
          )}
          <Button type="text" icon={<CloseOutlined />} onClick={onClose} />
        </span>
      }
    >
      {log && (
        <div className="space-y-3">
          <div>
            <Tag>{log.level}</Tag>
          </div>

          {viewMode === 'json' ? (
            <div className="space-y-2">
              <div className="flex justify-end">
                <Button
                  size="small"
                  icon={<CopyOutlined />}
                  loading={isCopying}
                  onClick={handleCopy}
                >
                  Copy
                </Button>
              </div>
              <div className="max-h-[70vh] overflow-auto rounded border border-gray-200 dark:border-gray-700">
                <SyntaxHighlighter
                  language="json"
                  style={tomorrow}
                  customStyle={{ margin: 0, fontSize: '12px' }}
                >
                  {jsonText}
                </SyntaxHighlighter>
              </div>
            </div>
          ) : (
            <Table<DetailRow>
              size="small"
              pagination={false}
              dataSource={rows}
              rowKey="key"
              columns={[
                {
                  title: 'Field',
                  dataIndex: 'key',
                  width: 160,
                  render: (v: string) => (
                    <span className="!font-mono text-xs font-medium">{v}</span>
                  ),
                },
                {
                  title: 'Value',
                  dataIndex: 'value',
                  render: (value: string, row: DetailRow) =>
                    row.isCustomField && onAddFieldToQuery ? (
                      <div className="group flex items-start gap-1">
                        <button
                          type="button"
                          onClick={() => onAddFieldToQuery(row.key, value)}
                          className="min-w-0 flex-1 rounded px-1 py-0.5 text-left !font-mono text-xs break-all whitespace-pre-wrap text-gray-800 hover:bg-emerald-50 dark:text-gray-100 dark:hover:bg-emerald-900/40"
                          title={`Show only: ${row.key}:${value}`}
                        >
                          {value}
                        </button>
                        {onExcludeFieldFromQuery && (
                          <button
                            type="button"
                            onClick={() => onExcludeFieldFromQuery(row.key, value)}
                            className="shrink-0 rounded px-1 text-xs text-gray-400 opacity-0 group-hover:opacity-100 hover:bg-red-50 hover:text-red-600 dark:hover:bg-red-900/30"
                            title={`Hide: ${row.key}:${value}`}
                            aria-label={`Hide logs where ${row.key} is ${value}`}
                          >
                            −
                          </button>
                        )}
                      </div>
                    ) : (
                      <span className="!font-mono text-xs break-all whitespace-pre-wrap">
                        {value}
                      </span>
                    ),
                },
              ]}
            />
          )}
        </div>
      )}
    </Drawer>
  );
};
