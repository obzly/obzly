import { LoadingOutlined } from '@ant-design/icons';
import { Drawer, Spin, Tag, Timeline } from 'antd';
import dayjs from 'dayjs';
import React, { useEffect, useState } from 'react';

import { type AiUsageSession, type AiUsageSessionEvent, aiUsageApi } from '../../../entity/aiUsage';
import { colorFor, describeEvent } from '../lib/describeEvent';
import { formatCount, formatUsd } from '../lib/format';

interface Props {
  projectId: string;
  session: AiUsageSession | null;
  onClose: () => void;
}

const HIDDEN_EVENTS = new Set(['assistant_response']);

export const SessionDrawer = ({ projectId, session, onClose }: Props): React.JSX.Element => {
  const [events, setEvents] = useState<AiUsageSessionEvent[]>([]);
  const [isLoading, setIsLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [truncated, setTruncated] = useState(false);

  useEffect(() => {
    if (!session) return;
    let cancelled = false;
    setIsLoading(true);
    aiUsageApi
      .getSessionEvents(projectId, session.sessionId, {
        from: dayjs(session.firstSeen).subtract(1, 'minute').toISOString(),
        to: dayjs(session.lastSeen).add(1, 'minute').toISOString(),
      })
      .then((response) => {
        if (cancelled) return;
        setEvents(response.events);
        setTruncated(response.truncated);
        setError(null);
      })
      .catch((e: Error) => !cancelled && setError(e.message))
      .finally(() => !cancelled && setIsLoading(false));
    return () => {
      cancelled = true;
    };
  }, [projectId, session]);

  const visible = events.filter((e) => !HIDDEN_EVENTS.has(e.event));

  return (
    <Drawer
      open={!!session}
      onClose={onClose}
      width={640}
      title={session ? `Session ${session.sessionId.slice(0, 8)}` : ''}
    >
      {session && (
        <div className="mb-4 space-y-1 text-sm text-gray-600 dark:text-gray-300">
          <div>{session.user}</div>
          <div>
            {session.models.map((model) => (
              <Tag key={model}>{model}</Tag>
            ))}
          </div>
          <div>
            {formatUsd(session.costUsd)} est. · {formatCount(session.tokens)} tokens ·{' '}
            {session.requests} requests · {session.toolCalls} tool calls
          </div>
          <div className="font-mono text-xs text-gray-400 dark:text-gray-500">
            {session.sessionId}
          </div>
        </div>
      )}
      {isLoading && <Spin indicator={<LoadingOutlined spin />} />}
      {error && <div className="text-sm text-red-600 dark:text-red-400">{error}</div>}
      {!isLoading && !error && (
        <Timeline
          items={visible.map((e) => {
            const { event, fields } = e;
            return {
              color: colorFor(event, fields),
              children: (
                <div className="text-sm">
                  <span className="mr-2 font-mono text-xs text-gray-400 dark:text-gray-500">
                    {dayjs(e.timestamp).format('HH:mm:ss')}
                  </span>
                  <span className="text-gray-700 dark:text-gray-200">
                    {describeEvent(event, fields)}
                  </span>
                </div>
              ),
            };
          })}
        />
      )}
      {truncated && (
        <div className="text-xs text-gray-500 dark:text-gray-400">
          Showing the first {events.length} events.
        </div>
      )}
    </Drawer>
  );
};
