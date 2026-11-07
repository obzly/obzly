import { CopyOutlined } from '@ant-design/icons';
import { App, Button, Empty, Spin, Tag } from 'antd';
import dayjs from 'dayjs';
import React, { useEffect, useState } from 'react';

import {
  type Session,
  type SessionDetail,
  type SignalDetail,
  correlationApi,
} from '../../../entity/correlation';
import { colorForService } from '../../traces/ui/traceColors';
import { formatWindow, triggerTypeColor, triggerTypeLabel } from './sessionFormat';

interface Props {
  projectId: string;
  sessionId: string;
  onBack: () => void;
}

const formatTimestamp = (value: string): string => dayjs(value).format('YYYY-MM-DD HH:mm:ss.SSS');

// Same colors as QueryResultsComponent's renderLogLevel.
const LOG_LEVEL_COLORS: Record<string, string> = {
  ERROR: 'bg-red-100 text-red-800 border-red-200',
  WARN: 'bg-yellow-100 text-yellow-800 border-yellow-200',
  INFO: 'bg-blue-100 text-blue-800 border-blue-200',
  DEBUG:
    'bg-gray-100 dark:bg-gray-700 text-gray-800 dark:text-gray-100 border-gray-200 dark:border-gray-700',
  TRACE: 'bg-purple-100 text-purple-800 border-purple-200',
  FATAL: 'bg-red-200 text-red-900 border-red-300',
  CRITICAL: 'bg-red-200 text-red-900 border-red-300',
};

const renderLogLevelBadge = (level: string) => (
  <span
    className={`inline-block rounded border px-1 py-0.5 text-xs font-medium ${LOG_LEVEL_COLORS[level.toUpperCase()] || LOG_LEVEL_COLORS.INFO}`}
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
    <Tag className="cursor-pointer font-mono" onClick={handleCopy} title={value}>
      {label}: {value.slice(0, 12)}… <CopyOutlined className="text-xs" />
    </Tag>
  );
};

const SignalRow = ({ signal }: { signal: SignalDetail }): React.JSX.Element => {
  if (!signal.available) {
    return (
      <div className="rounded border border-dashed border-gray-300 bg-gray-50 p-3 text-sm text-gray-400 italic dark:border-gray-600 dark:bg-gray-900/40 dark:text-gray-500">
        {signal.signalType} signal from {formatTimestamp(signal.occurredAt)} — no longer available
        (likely removed by retention)
      </div>
    );
  }

  if (signal.signalType === 'log') {
    return (
      <div className="rounded border border-gray-200 p-3 dark:border-gray-700">
        <div className="mb-1 flex items-center gap-2">
          {signal.log && renderLogLevelBadge(signal.log.level)}
          <span className="text-xs text-gray-400">{formatTimestamp(signal.occurredAt)}</span>
        </div>
        <div className="text-sm text-gray-800 dark:text-gray-100">
          {signal.log?.message ?? <span className="text-gray-400 italic">No message</span>}
        </div>
      </div>
    );
  }

  if (signal.signalType === 'span') {
    const span = signal.span;
    const isError = span?.statusCode === 'ERROR';
    return (
      <div className="rounded border border-gray-200 p-3 dark:border-gray-700">
        <div className="mb-1 flex items-center justify-between">
          <div className="flex items-center gap-2">
            <span className="font-medium text-gray-800 dark:text-gray-100">
              {span?.name ?? 'Unnamed span'}
            </span>
            {span && (
              <Tag color={colorForService(span.serviceName)} variant="solid">
                {span.serviceName}
              </Tag>
            )}
            {span && <Tag color={isError ? 'red' : 'green'}>{span.statusCode}</Tag>}
          </div>
          <span className="text-xs text-gray-400">{formatTimestamp(signal.occurredAt)}</span>
        </div>
        {span && (
          <div className="flex flex-wrap gap-2">
            <CopyableRef label="Trace" value={span.traceId} />
            <CopyableRef label="Span" value={span.spanId} />
          </div>
        )}
      </div>
    );
  }

  // e.g. "metric", which has no content DTO yet.
  return (
    <div className="rounded border border-gray-200 p-3 dark:border-gray-700">
      <div className="flex items-center justify-between text-sm">
        <span className="text-gray-600 dark:text-gray-300">{signal.signalType} signal</span>
        <span className="text-xs text-gray-400">{formatTimestamp(signal.occurredAt)}</span>
      </div>
    </div>
  );
};

export const SessionDetailComponent = ({
  projectId,
  sessionId,
  onBack,
}: Props): React.JSX.Element => {
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
    <div>
      <Button onClick={onBack} className="mb-4">
        ← Back to sessions
      </Button>

      {isLoading && (
        <div className="flex justify-center py-8">
          <Spin />
        </div>
      )}

      {!isLoading && session && (
        <>
          <div className="mb-4 rounded border border-gray-200 p-4 dark:border-gray-700">
            <div className="mb-2 flex flex-wrap items-center gap-2">
              <Tag color={colorForService(session.serviceName)} variant="solid">
                {session.serviceName}
              </Tag>
              <Tag color={triggerTypeColor(session.triggerType)}>
                {triggerTypeLabel(session.triggerType)}
              </Tag>
              <span className="text-xs text-gray-400">
                Created {formatTimestamp(session.createdAt)}
              </span>
            </div>
            <div className="text-sm text-gray-600 dark:text-gray-300">
              Window: {formatWindow(session.windowStart, session.windowEnd)}
            </div>
            {session.triggerSummary && (
              <div className="mt-2 text-sm text-gray-800 dark:text-gray-100">
                {session.triggerSummary}
              </div>
            )}
          </div>

          {signals.length === 0 ? (
            <Empty description="No signals recorded for this session" />
          ) : (
            <div className="space-y-2">
              {signals.map((signal, index) => (
                <SignalRow
                  key={`${signal.signalType}-${signal.occurredAt}-${index}`}
                  signal={signal}
                />
              ))}
            </div>
          )}
        </>
      )}

      {!isLoading && !session && <Empty description="Session not found" />}
    </div>
  );
};
