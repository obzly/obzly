import { LoadingOutlined, ReloadOutlined } from '@ant-design/icons';
import { Alert, Button, Spin, Tooltip } from 'antd';
import { type JSX, useEffect, useRef, useState } from 'react';

import {
  type IngestionHealth,
  type IngestionStatus,
  projectApi,
} from '../../../../entity/projects';
import { Card } from '../../../../shared/ui/v2';
import { Pill, type PillTone } from '../ProjectPageShell';
import { DataSourceIcon } from './DataSourceIcon';
import { dataSourceCatalog } from './catalog';
import { HEALTH_LABEL, formatAgo, locationText, signalChip } from './ingestionStatusFormat';

interface Props {
  projectId: string;
}

const REFRESH_MS = 30_000;

// Good is green, bad is red: stopped is the bad state, quiet a warning.
const HEALTH_TONE: Record<IngestionHealth, PillTone> = {
  sending: 'ok',
  quiet: 'warn',
  stopped: 'bad',
};

const catalogNames = new Map(dataSourceCatalog.map((d) => [d.id, d.name]));

/**
 * What is sending to the project right now: one card per source with its
 * health, signals, recent rate and last data. Refreshes every 30 s while the
 * tab is visible.
 */
export const IngestionStatusComponent = ({ projectId }: Props): JSX.Element => {
  const [status, setStatus] = useState<IngestionStatus | undefined>(undefined);
  const [error, setError] = useState<string | undefined>(undefined);
  const [isLoading, setIsLoading] = useState(true);
  const [now, setNow] = useState(() => Date.now());
  const requestId = useRef(0);

  const load = async () => {
    const id = ++requestId.current;
    try {
      const loaded = await projectApi.getIngestionStatus(projectId);
      if (id !== requestId.current) return;
      setStatus(loaded);
      setError(undefined);
    } catch (e: unknown) {
      if (id !== requestId.current) return;
      setError(e instanceof Error ? e.message : 'Failed to load ingestion status');
    } finally {
      if (id === requestId.current) {
        setIsLoading(false);
        setNow(Date.now());
      }
    }
  };

  useEffect(() => {
    setStatus(undefined);
    setIsLoading(true);
    load();
    let timer: ReturnType<typeof setInterval> | undefined;
    const start = () => {
      if (timer === undefined) timer = setInterval(load, REFRESH_MS);
    };
    const stop = () => {
      if (timer !== undefined) clearInterval(timer);
      timer = undefined;
    };
    const onVisibility = () => {
      if (document.visibilityState === 'visible') {
        load();
        start();
      } else {
        stop();
      }
    };
    if (document.visibilityState === 'visible') start();
    document.addEventListener('visibilitychange', onVisibility);
    return () => {
      stop();
      document.removeEventListener('visibilitychange', onVisibility);
      requestId.current++;
    };
  }, [projectId]);

  const sources = status?.sources ?? [];
  const sending = sources.filter((s) => s.health === 'sending').length;

  return (
    <Card
      title="Ingestion status"
      subtitle={
        status
          ? `Each source sending to this project, its health and its rate over the last ${status.rateWindowMinutes} minutes. Quiet: nothing for ${status.quietAfterMinutes} minutes; stopped: nothing for ${status.stoppedAfterHours} hours.`
          : 'Each source sending to this project, its health and its recent rate.'
      }
      extra={
        <>
          {status && sources.length > 0 && (
            <span className="text-xs text-gray-500 dark:text-gray-400">
              {sending} of {sources.length} sending
            </span>
          )}
          <Tooltip title="Refresh now">
            <Button
              size="small"
              type="text"
              icon={<ReloadOutlined />}
              aria-label="Refresh ingestion status"
              onClick={() => load()}
            />
          </Tooltip>
        </>
      }
    >
      {error && (
        <Alert
          type="error"
          showIcon
          className="!mb-3"
          message={status ? `Could not refresh: ${error}` : error}
        />
      )}

      {isLoading && !status && (
        <div className="flex justify-center py-6">
          <Spin indicator={<LoadingOutlined spin />} />
        </div>
      )}

      {status && sources.length === 0 && (
        <div className="rounded-xl border border-dashed border-gray-300 px-4 py-6 text-center text-sm text-gray-500 dark:border-gray-600 dark:text-gray-400">
          No data yet — pick a source below
        </div>
      )}

      {sources.length > 0 && (
        <div className="grid grid-cols-[repeat(auto-fill,minmax(min(100%,250px),1fr))] gap-3">
          {sources.map((source) => {
            const typeName = source.dataSource ? catalogNames.get(source.dataSource) : undefined;
            const where = locationText(source);
            return (
              <article
                key={source.id}
                aria-label={source.name}
                className="flex min-w-0 flex-col gap-2 rounded-xl border border-gray-200 p-4 dark:border-gray-700"
              >
                <span className="flex items-start justify-between gap-2">
                  <span className="flex h-9 w-9 shrink-0 items-center justify-center rounded-[10px] border border-gray-200 bg-gray-50 dark:border-gray-700 dark:bg-gray-900/40">
                    <DataSourceIcon sourceId={source.dataSource || 'custom-otlp'} size={18} />
                  </span>
                  <Pill tone={HEALTH_TONE[source.health]}>{HEALTH_LABEL[source.health]}</Pill>
                </span>
                <span className="min-w-0">
                  <strong
                    className="block truncate font-semibold text-gray-900 dark:text-gray-50"
                    title={source.name}
                  >
                    {source.name}
                  </strong>
                  <span className="block truncate text-xs text-gray-500 dark:text-gray-400">
                    {[typeName, where].filter(Boolean).join(' · ') || 'Type and location unknown'}
                  </span>
                </span>
                <span className="flex flex-wrap gap-1.5">
                  {source.signals.map((signal) => (
                    <Tooltip
                      key={signal.signal}
                      title={`${signal.recentCount.toLocaleString()} in the last ${status?.rateWindowMinutes ?? 15} min · last ${formatAgo(signal.lastSeen, now)}`}
                    >
                      <span className="rounded-full bg-gray-100 px-2 py-0.5 text-xs text-gray-700 dark:bg-gray-700 dark:text-gray-200">
                        {signalChip(signal)}
                      </span>
                    </Tooltip>
                  ))}
                </span>
                <span className="text-xs text-gray-500 dark:text-gray-400">
                  last data {formatAgo(source.lastSeen, now)}
                </span>
              </article>
            );
          })}
        </div>
      )}

      {status?.truncated && (
        <div className="mt-3 text-xs text-gray-500 dark:text-gray-400">
          Showing the most active sources only.
        </div>
      )}
    </Card>
  );
};
