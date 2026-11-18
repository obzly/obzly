import { CaretDownOutlined, CaretRightOutlined } from '@ant-design/icons';
import React, { useMemo, useState } from 'react';

import type { Span } from '../../../entity/traces';
import { formatDurationMs, useServiceColor } from './traceColors';

interface Props {
  spans: Span[];
  /** Width of the service + span name column; narrower in a small side panel. */
  nameColumnWidth?: number;
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

export const TraceWaterfallComponent = ({
  spans,
  nameColumnWidth = 300,
}: Props): React.JSX.Element => {
  const serviceColor = useServiceColor();
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
    <div className="w-full overflow-x-auto">
      <div className="flex min-w-[520px] flex-col gap-0.5">
        {rows.map(({ span, depth }) => {
          const startOffsetMs = new Date(span.startTime).getTime() - traceStartMs;
          const leftPct = (startOffsetMs / totalDurationMs) * 100;
          const widthPct = Math.max((span.durationMs / totalDurationMs) * 100, 0.5);
          const isExpanded = expandedSpanId === span.spanId;
          const isError = span.statusCode === 'ERROR';
          const color = serviceColor(span.serviceName);

          return (
            <div key={span.spanId} className="flex flex-col">
              <button
                type="button"
                aria-expanded={isExpanded}
                className={`flex min-h-7 w-full cursor-pointer items-center gap-2 rounded-md border-0 px-1 text-left ${
                  isExpanded
                    ? 'bg-[var(--v2-ok-soft)]'
                    : 'bg-transparent hover:bg-gray-100 dark:hover:bg-gray-700/60'
                }`}
                onClick={() => setExpandedSpanId(isExpanded ? null : span.spanId)}
              >
                <span
                  className="flex shrink-0 items-center gap-1.5 overflow-hidden whitespace-nowrap"
                  style={{ width: nameColumnWidth, paddingLeft: depth * 14 }}
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
                  <span className="truncate text-xs text-gray-500 dark:text-gray-400">
                    {span.serviceName}
                  </span>
                  <span className="truncate text-[12.5px] text-gray-800 dark:text-gray-100">
                    {span.name}
                  </span>
                  {isError && (
                    <span className="shrink-0 rounded-full bg-red-500/15 px-1.5 text-[10.5px] text-[var(--v2-red)]">
                      error
                    </span>
                  )}
                </span>

                <span className="relative h-3.5 grow">
                  <span
                    className="absolute top-0.5 bottom-0.5 rounded"
                    style={{
                      left: `${leftPct}%`,
                      width: `${widthPct}%`,
                      backgroundColor: isError ? 'var(--v2-red)' : color,
                      opacity: 0.85,
                    }}
                    title={`${formatDurationMs(span.durationMs)}`}
                  />
                </span>

                <span className="w-[70px] shrink-0 text-right font-mono text-[11.5px] text-gray-500 dark:text-gray-400">
                  {formatDurationMs(span.durationMs)}
                </span>
              </button>

              {isExpanded && (
                <div className="mt-1 mb-2 ml-[18px] flex flex-col gap-2 rounded-xl bg-[var(--v2-panel2)] px-3 py-2.5 text-xs text-gray-800 dark:text-gray-100">
                  <dl className="m-0 grid grid-cols-[110px_minmax(0,1fr)] gap-x-3 gap-y-1">
                    <dt className="text-gray-500 dark:text-gray-400">Span ID</dt>
                    <dd className="m-0 font-mono break-all">{span.spanId}</dd>
                    <dt className="text-gray-500 dark:text-gray-400">Trace ID</dt>
                    <dd className="m-0 font-mono break-all">{span.traceId}</dd>
                    <dt className="text-gray-500 dark:text-gray-400">Kind</dt>
                    <dd className="m-0">{span.kind || '-'}</dd>
                    {span.statusMessage && (
                      <>
                        <dt className="text-gray-500 dark:text-gray-400">Status message</dt>
                        <dd className="m-0 text-[var(--v2-red)]">{span.statusMessage}</dd>
                      </>
                    )}
                  </dl>

                  {span.attributes && Object.keys(span.attributes).length > 0 && (
                    <div>
                      <div className="mb-1 font-semibold text-gray-700 dark:text-gray-200">
                        Attributes
                      </div>
                      <dl className="m-0 grid grid-cols-[minmax(0,200px)_minmax(0,1fr)] gap-x-3 gap-y-0.5">
                        {Object.entries(span.attributes).map(([key, value]) => (
                          <React.Fragment key={key}>
                            <dt className="truncate font-mono text-gray-500 dark:text-gray-400">
                              {key}
                            </dt>
                            <dd className="m-0 truncate font-mono" title={String(value)}>
                              {String(value)}
                            </dd>
                          </React.Fragment>
                        ))}
                      </dl>
                    </div>
                  )}

                  {span.events && span.events.length > 0 && (
                    <div>
                      <div className="mb-1 font-semibold text-gray-700 dark:text-gray-200">
                        Events
                      </div>
                      {span.events.map((event, i) => (
                        <div key={i} className="mb-1">
                          <span className="font-semibold">{event.name}</span>
                          {event.attributes &&
                            Object.entries(event.attributes).map(([key, value]) => (
                              <div key={key} className="ml-3 text-gray-500 dark:text-gray-400">
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
    </div>
  );
};
