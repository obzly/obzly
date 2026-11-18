import { CopyOutlined, LoadingOutlined } from '@ant-design/icons';
import { App, Button, Empty, Spin, Tag } from 'antd';
import dayjs from 'dayjs';
import React, { useEffect, useState } from 'react';

import {
  type ServiceImpact,
  type Session,
  type SessionDetail,
  type SignalDetail,
  correlationApi,
} from '../../../entity/correlation';
import { Card, RankedList } from '../../../shared/ui/v2';
import { useServiceColor } from '../../traces/ui/traceColors';
import { formatWindow, triggerTypeColor, triggerTypeLabel } from './sessionFormat';

interface Props {
  projectId: string;
  sessionId: string;
  onBack: () => void;
}

const formatTimestamp = (value: string): string => dayjs(value).format('YYYY-MM-DD HH:mm:ss.SSS');

// Same hues as QueryResultsComponent's renderLogLevel, with dark variants.
const LOG_LEVEL_COLORS: Record<string, string> = {
  ERROR:
    'bg-red-100 text-red-800 border-red-200 dark:bg-red-500/15 dark:text-red-300 dark:border-red-500/30',
  WARN: 'bg-yellow-100 text-yellow-800 border-yellow-200 dark:bg-yellow-500/15 dark:text-yellow-300 dark:border-yellow-500/30',
  INFO: 'bg-blue-100 text-blue-800 border-blue-200 dark:bg-blue-500/15 dark:text-blue-300 dark:border-blue-500/30',
  DEBUG:
    'bg-gray-100 dark:bg-gray-700 text-gray-800 dark:text-gray-100 border-gray-200 dark:border-gray-700',
  TRACE:
    'bg-purple-100 text-purple-800 border-purple-200 dark:bg-purple-500/15 dark:text-purple-300 dark:border-purple-500/30',
  FATAL:
    'bg-red-200 text-red-900 border-red-300 dark:bg-red-500/25 dark:text-red-200 dark:border-red-500/40',
  CRITICAL:
    'bg-red-200 text-red-900 border-red-300 dark:bg-red-500/25 dark:text-red-200 dark:border-red-500/40',
};

const renderLogLevelBadge = (level: string) => (
  <span
    className={`inline-block shrink-0 rounded-full border px-2 py-0.5 text-[11px] font-medium ${LOG_LEVEL_COLORS[level.toUpperCase()] || LOG_LEVEL_COLORS.INFO}`}
  >
    {level}
  </span>
);

// Copy-to-clipboard, since there is no deep link from Incidents into Traces.
const CopyableRef = ({ label, value }: { label: string; value: string }) => {
  const { message } = App.useApp();
  const handleCopy = async () => {
    try {
      await navigator.clipboard.writeText(value);
      message.success(`${label} copied`);
    } catch {
      message.error(`Could not copy ${label.toLowerCase()}`);
    }
  };
  return (
    <button
      type="button"
      onClick={handleCopy}
      title={value}
      aria-label={`Copy ${label.toLowerCase()} ID`}
      className="hover:border-accent-500/60 inline-flex cursor-pointer items-center gap-1.5 rounded-lg border border-gray-200 bg-gray-50 px-2.5 py-1 font-mono text-[11.5px] text-gray-800 dark:border-gray-700 dark:bg-gray-900 dark:text-gray-100"
    >
      {label}: {value.slice(0, 12)}… <CopyOutlined className="text-xs" />
    </button>
  );
};

const SignalContent = ({ signal }: { signal: SignalDetail }): React.JSX.Element => {
  const serviceColor = useServiceColor();
  if (!signal.available) {
    return (
      <div className="rounded-lg border border-dashed border-gray-300 bg-gray-50 px-3 py-2 text-sm text-gray-400 italic dark:border-gray-600 dark:bg-gray-900/40 dark:text-gray-500">
        {signal.signalType} signal from {formatTimestamp(signal.occurredAt)} — no longer available
        (likely removed by retention)
      </div>
    );
  }

  if (signal.signalType === 'log') {
    return (
      <div className="flex items-baseline gap-2">
        {signal.log && renderLogLevelBadge(signal.log.level)}
        <span className="font-mono text-[12.5px] [overflow-wrap:anywhere] text-gray-800 dark:text-gray-100">
          {signal.log?.message ?? <span className="text-gray-400 italic">No message</span>}
        </span>
      </div>
    );
  }

  if (signal.signalType === 'span') {
    const span = signal.span;
    const isError = span?.statusCode === 'ERROR';
    return (
      <div className="flex flex-col gap-1.5">
        <div className="flex flex-wrap items-center gap-2">
          <span className="font-medium text-gray-800 dark:text-gray-100">
            {span?.name ?? 'Unnamed span'}
          </span>
          {span && (
            <Tag color={serviceColor(span.serviceName)} variant="solid" className="rounded-full">
              {span.serviceName}
            </Tag>
          )}
          {span && (
            <Tag color={isError ? 'red' : 'green'} className="rounded-full">
              {span.statusCode}
            </Tag>
          )}
        </div>
        {span && (
          <div className="flex flex-wrap gap-1.5">
            <CopyableRef label="Trace" value={span.traceId} />
            <CopyableRef label="Span" value={span.spanId} />
          </div>
        )}
      </div>
    );
  }

  // e.g. "metric", which has no content DTO yet.
  return (
    <span className="text-sm text-gray-600 dark:text-gray-300">{signal.signalType} signal</span>
  );
};

// One timeline row: when on the left, what on the right.
const SignalRow = ({ signal }: { signal: SignalDetail }): React.JSX.Element => (
  <div className="flex flex-wrap gap-x-4 gap-y-1 border-t border-gray-200 py-2.5 first:border-t-0 dark:border-gray-700">
    <span className="w-[178px] shrink-0 pt-0.5 font-mono text-xs text-gray-500 dark:text-gray-400">
      {formatTimestamp(signal.occurredAt)}
    </span>
    <div className="min-w-[240px] flex-1">
      <SignalContent signal={signal} />
    </div>
  </div>
);

const impactBreakdown = (row: ServiceImpact): string =>
  [
    `${row.errorLogs.toLocaleString()} log${row.errorLogs === 1 ? '' : 's'}`,
    `${row.errorSpans.toLocaleString()} span${row.errorSpans === 1 ? '' : 's'}`,
  ].join(' · ');

// Error logs and spans per service in the session window, counted live.
const ImpactBySessionService = ({
  projectId,
  sessionId,
}: {
  projectId: string;
  sessionId: string;
}): React.JSX.Element => {
  const serviceColor = useServiceColor();
  const [impact, setImpact] = useState<ServiceImpact[] | null>(null);
  const [isLoading, setIsLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    let cancelled = false;
    setIsLoading(true);
    setError(null);
    correlationApi
      .getSessionImpact(projectId, sessionId)
      .then((response) => {
        if (!cancelled) setImpact(response.services);
      })
      .catch((e: unknown) => {
        if (!cancelled) setError((e as Error).message);
      })
      .finally(() => {
        if (!cancelled) setIsLoading(false);
      });
    return () => {
      cancelled = true;
    };
  }, [projectId, sessionId]);

  return (
    <Card
      className="flex-[1_1_320px]"
      title="Impact by service"
      subtitle="Error logs and error spans in the session window"
    >
      {isLoading && (
        <div className="flex justify-center py-4">
          <Spin indicator={<LoadingOutlined spin />} />
        </div>
      )}
      {!isLoading && error && (
        <div className="py-2 text-sm text-red-500 dark:text-red-400">
          Could not load the impact: {error}
        </div>
      )}
      {!isLoading && !error && impact && (
        <RankedList
          empty="No services recorded for this session"
          rows={impact.map((row) => ({
            key: row.serviceName,
            label: row.serviceName,
            value: row.total,
            extra: impactBreakdown(row),
            color: serviceColor(row.serviceName),
          }))}
        />
      )}
    </Card>
  );
};

export const SessionDetailComponent = ({
  projectId,
  sessionId,
  onBack,
}: Props): React.JSX.Element => {
  const serviceColor = useServiceColor();
  const { message } = App.useApp();

  const [isLoading, setIsLoading] = useState(true);
  const [session, setSession] = useState<Session | null>(null);
  const [signals, setSignals] = useState<SignalDetail[]>([]);

  useEffect(() => {
    let cancelled = false;
    setIsLoading(true);

    correlationApi
      .getSessionDetail(projectId, sessionId)
      .then((detail: SessionDetail) => {
        if (cancelled) return;
        setSession(detail.session);
        // The backend doesn't guarantee an order.
        setSignals(
          [...detail.signals].sort(
            (a, b) => new Date(a.occurredAt).getTime() - new Date(b.occurredAt).getTime(),
          ),
        );
      })
      .catch((e: unknown) => message.error((e as Error).message))
      .finally(() => {
        if (!cancelled) setIsLoading(false);
      });

    return () => {
      cancelled = true;
    };
  }, [projectId, sessionId]);

  return (
    <div className="flex flex-col gap-4">
      <div>
        <Button onClick={onBack}>← Back to sessions</Button>
      </div>

      {isLoading && (
        <div className="flex justify-center py-8">
          <Spin />
        </div>
      )}

      {!isLoading && session && (
        <>
          <Card bodyClassName="flex flex-col gap-2.5">
            <div className="flex flex-wrap items-center gap-2">
              <Tag
                color={serviceColor(session.serviceName)}
                variant="solid"
                className="rounded-full"
              >
                {session.serviceName}
              </Tag>
              <Tag color={triggerTypeColor(session.triggerType)} className="rounded-full">
                {triggerTypeLabel(session.triggerType)}
              </Tag>
              <span className="text-xs text-gray-500 dark:text-gray-400">
                Created {formatTimestamp(session.createdAt)}
              </span>
              <span className="text-xs text-gray-500 dark:text-gray-400">
                Window: {formatWindow(session.windowStart, session.windowEnd)}
              </span>
            </div>
            {session.triggerSummary && (
              <div className="text-[15px] text-gray-900 dark:text-gray-50">
                {session.triggerSummary}
              </div>
            )}
          </Card>

          <div className="flex flex-wrap items-start gap-4">
            <Card className="min-w-0 flex-[999_1_480px]" title="Signals" subtitle="Oldest first">
              {signals.length === 0 ? (
                <Empty description="No signals recorded for this session" className="py-4" />
              ) : (
                <div>
                  {signals.map((signal, index) => (
                    <SignalRow
                      key={`${signal.signalType}-${signal.occurredAt}-${index}`}
                      signal={signal}
                    />
                  ))}
                </div>
              )}
            </Card>
            <ImpactBySessionService projectId={projectId} sessionId={sessionId} />
          </div>
        </>
      )}

      {!isLoading && !session && (
        <Card>
          <Empty description="Session not found" />
        </Card>
      )}
    </div>
  );
};
