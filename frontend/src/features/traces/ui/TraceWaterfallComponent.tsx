import { CaretDownOutlined, CaretRightOutlined } from '@ant-design/icons';
import { Tag } from 'antd';
import React, { useMemo, useState } from 'react';

import type { Span } from '../../../entity/traces';
import { colorForService, formatDurationMs } from './traceColors';

interface Props {
  spans: Span[];
}

interface SpanRow {
  span: Span;
  depth: number;
}

function buildRows(spans: Span[]): SpanRow[] {
  const spanIds = new Set(spans.map((s) => s.spanId));
  const childrenByParent = new Map<string, Span[]>();
  const roots: Span[] = [];

  for (const span of spans) {
    if (span.parentSpanId && spanIds.has(span.parentSpanId)) {
      const siblings = childrenByParent.get(span.parentSpanId) ?? [];
      siblings.push(span);
      childrenByParent.set(span.parentSpanId, siblings);
    } else {
      roots.push(span);
    }
  }

  const byStart = (a: Span, b: Span) =>
    new Date(a.startTime).getTime() - new Date(b.startTime).getTime();
  roots.sort(byStart);
  for (const siblings of childrenByParent.values()) siblings.sort(byStart);

  const rows: SpanRow[] = [];
  const visit = (span: Span, depth: number) => {
    rows.push({ span, depth });
    for (const child of childrenByParent.get(span.spanId) ?? []) visit(child, depth + 1);
  };
  for (const root of roots) visit(root, 0);

  return rows;
}

export const TraceWaterfallComponent = ({ spans }: Props): React.JSX.Element => {
  const [expandedSpanId, setExpandedSpanId] = useState<string | null>(null);

  const rows = useMemo(() => buildRows(spans), [spans]);

  const { traceStartMs, totalDurationMs } = useMemo(() => {
    if (spans.length === 0) return { traceStartMs: 0, totalDurationMs: 1 };
    const starts = spans.map((s) => new Date(s.startTime).getTime());
    const ends = spans.map((s) => new Date(s.endTime).getTime());
    const start = Math.min(...starts);
    const end = Math.max(...ends);
    return { traceStartMs: start, totalDurationMs: Math.max(end - start, 1) };
  }, [spans]);

  if (spans.length === 0) {
    return (
      <div className="py-8 text-center text-gray-400 dark:text-gray-400">
        No spans in this trace
      </div>
    );
  }

  return (
    <div className="w-full">
      {rows.map(({ span, depth }) => {
        const startOffsetMs = new Date(span.startTime).getTime() - traceStartMs;
        const leftPct = (startOffsetMs / totalDurationMs) * 100;
        const widthPct = Math.max((span.durationMs / totalDurationMs) * 100, 0.5);
        const isExpanded = expandedSpanId === span.spanId;
        const isError = span.statusCode === 'ERROR';
        const color = colorForService(span.serviceName);

        return (
          <div key={span.spanId} className="border-b border-gray-100 last:border-b-0">
            <div
              className="flex cursor-pointer items-center gap-2 py-1.5 hover:bg-gray-50 dark:bg-gray-900 dark:hover:bg-gray-700"
              onClick={() => setExpandedSpanId(isExpanded ? null : span.spanId)}
            >
              <div
                className="flex shrink-0 items-center gap-1 overflow-hidden"
                style={{ width: 280, paddingLeft: depth * 16 }}
              >
                {isExpanded ? (
                  <CaretDownOutlined className="shrink-0 text-gray-400 dark:text-gray-400" />
                ) : (
                  <CaretRightOutlined className="shrink-0 text-gray-400 dark:text-gray-400" />
                )}
                <span
                  className="h-2 w-2 shrink-0 rounded-full"
                  style={{ backgroundColor: color }}
                />
                <span className="truncate text-xs font-medium text-gray-500 dark:text-gray-300">
                  {span.serviceName}
                </span>
                <span className="truncate text-sm text-gray-800 dark:text-gray-100">
                  {span.name}
                </span>
                {isError && (
                  <Tag color="red" className="shrink-0 text-xs">
                    error
                  </Tag>
                )}
              </div>

              <div className="relative h-4 grow">
                <div
                  className="absolute top-0 h-4 rounded"
                  style={{
                    left: `${leftPct}%`,
                    width: `${widthPct}%`,
                    backgroundColor: isError ? '#dc2626' : color,
                    opacity: 0.85,
                  }}
                  title={`${formatDurationMs(span.durationMs)}`}
                />
              </div>

              <div className="w-20 shrink-0 text-right text-xs text-gray-500 dark:text-gray-300">
                {formatDurationMs(span.durationMs)}
              </div>
            </div>

            {isExpanded && (
              <div className="mb-2 ml-4 space-y-2 rounded bg-gray-50 p-3 text-xs dark:bg-gray-900">
                <div className="grid grid-cols-[100px_1fr] gap-y-1">
                  <span className="text-gray-500 dark:text-gray-300">Span ID</span>
                  <span className="font-mono">{span.spanId}</span>
                  <span className="text-gray-500 dark:text-gray-300">Trace ID</span>
                  <span className="font-mono">{span.traceId}</span>
                  <span className="text-gray-500 dark:text-gray-300">Kind</span>
                  <span>{span.kind || '-'}</span>
                  {span.statusMessage && (
                    <>
                      <span className="text-gray-500 dark:text-gray-300">Status message</span>
                      <span className="text-red-600">{span.statusMessage}</span>
                    </>
                  )}
                </div>

                {span.attributes && Object.keys(span.attributes).length > 0 && (
                  <div>
                    <div className="mb-1 font-medium text-gray-600 dark:text-gray-300">
                      Attributes
                    </div>
                    <div className="grid grid-cols-[200px_1fr] gap-y-0.5">
                      {Object.entries(span.attributes).map(([key, value]) => (
                        <React.Fragment key={key}>
                          <span className="truncate text-gray-500 dark:text-gray-300">{key}</span>
                          <span className="truncate font-mono">{String(value)}</span>
                        </React.Fragment>
                      ))}
                    </div>
                  </div>
                )}

                {span.events && span.events.length > 0 && (
                  <div>
                    <div className="mb-1 font-medium text-gray-600 dark:text-gray-300">Events</div>
                    {span.events.map((event, i) => (
                      <div key={i} className="mb-1">
                        <span className="font-medium">{event.name}</span>
                        {event.attributes &&
                          Object.entries(event.attributes).map(([key, value]) => (
                            <div key={key} className="ml-3 text-gray-500 dark:text-gray-300">
                              {key}: <span className="font-mono">{String(value)}</span>
                            </div>
                          ))}
                      </div>
                    ))}
                  </div>
                )}
              </div>
            )}
          </div>
        );
      })}
    </div>
  );
};
