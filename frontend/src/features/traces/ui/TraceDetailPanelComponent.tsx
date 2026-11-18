import { CloseOutlined, LoadingOutlined } from '@ant-design/icons';
import { Button, Spin, Tooltip } from 'antd';
import React, { useMemo } from 'react';

import type { Span } from '../../../entity/traces';
import { TraceWaterfallComponent } from './TraceWaterfallComponent';
import { formatDurationMs, useServiceColor } from './traceColors';

interface Props {
  traceId: string;
  spans: Span[] | null;
  isLoading: boolean;
  onClose: () => void;
  nameColumnWidth?: number;
  /** Inside a table row: no card border or close button (the row toggles it). */
  embedded?: boolean;
}

const summarize = (spans: Span[]) => {
  const ids = new Set(spans.map((s) => s.spanId));
  const roots = spans
    .filter((s) => !s.parentSpanId || !ids.has(s.parentSpanId))
    .sort((a, b) => new Date(a.startTime).getTime() - new Date(b.startTime).getTime());
  const start = Math.min(...spans.map((s) => new Date(s.startTime).getTime()));
  const end = Math.max(...spans.map((s) => new Date(s.endTime).getTime()));
  return {
    title: roots[0]?.name ?? spans[0]?.name ?? '',
    durationMs: Math.max(end - start, 0),
    errorCount: spans.filter((s) => s.statusCode === 'ERROR').length,
    services: Array.from(new Set(spans.map((s) => s.serviceName))),
  };
};

/** One trace's header, service legend and span waterfall. */
export const TraceDetailPanelComponent = ({
  traceId,
  spans,
  isLoading,
  onClose,
  nameColumnWidth,
  embedded = false,
}: Props): React.JSX.Element => {
  const serviceColor = useServiceColor();
  const summary = useMemo(() => (spans && spans.length > 0 ? summarize(spans) : null), [spans]);
  const isReady = !isLoading && spans !== null;

  const body = (
    <>
      {summary && (
        <div className="flex flex-wrap gap-x-3 gap-y-1 text-xs text-gray-500 dark:text-gray-400">
          {summary.services.map((service) => (
            <span key={service} className="inline-flex items-center gap-1.5">
              <i
                className="inline-block h-2.5 w-2.5 rounded-sm"
                style={{ backgroundColor: serviceColor(service) }}
              />
              {service}
            </span>
          ))}
        </div>
      )}
      {!isLoading && spans ? (
        <TraceWaterfallComponent spans={spans} nameColumnWidth={nameColumnWidth} />
      ) : (
        <div className="flex justify-center py-8">
          <Spin indicator={<LoadingOutlined spin />} />
        </div>
      )}
    </>
  );

  if (embedded) {
    return (
      <div className="flex flex-col gap-3 rounded-xl border border-gray-200 bg-white p-3 dark:border-gray-700 dark:bg-gray-800">
        {body}
      </div>
    );
  }

  return (
    <aside
      aria-label={`Trace ${traceId}`}
      className="flex min-w-0 flex-col gap-3 rounded-2xl border border-gray-200 bg-white p-5 dark:border-gray-700 dark:bg-gray-800"
    >
      <div className="flex items-start justify-between gap-2">
        <div className="min-w-0">
          <h2 className="m-0 text-[15px] font-semibold text-gray-900 dark:text-gray-50">
            Trace {traceId.slice(0, 16)}…
          </h2>
          {summary && (
            <p className="m-0 mt-0.5 truncate text-xs text-gray-500 dark:text-gray-400">
              {summary.title} · {formatDurationMs(summary.durationMs)} · {spans?.length ?? 0} spans
            </p>
          )}
        </div>
        <span className="flex shrink-0 items-center gap-2">
          {summary && isReady && (
            <span
              className={`rounded-full px-2 py-0.5 text-xs font-medium ${
                summary.errorCount > 0
                  ? 'bg-red-500/15 text-[var(--v2-red)]'
                  : 'bg-emerald-600/10 text-emerald-600 dark:bg-emerald-500/15 dark:text-emerald-500'
              }`}
            >
              {summary.errorCount > 0
                ? `${summary.errorCount} error${summary.errorCount === 1 ? '' : 's'}`
                : 'OK'}
            </span>
          )}
          <Tooltip title="Close trace">
            <Button
              size="small"
              icon={<CloseOutlined />}
              aria-label="Close trace"
              onClick={onClose}
            />
          </Tooltip>
        </span>
      </div>
      {body}
    </aside>
  );
};
