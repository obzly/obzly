import { LoadingOutlined, ReloadOutlined } from '@ant-design/icons';
import { Alert, Button, Spin, Tooltip } from 'antd';
import { type JSX, type ReactNode, useEffect, useMemo, useRef, useState } from 'react';

import {
  type IngestionHealth,
  type IngestionSource,
  type IngestionStatus,
  projectApi,
} from '../../../../entity/projects';
import { Card } from '../../../../shared/ui/v2';
import { IngestionSourcesDrawer } from './IngestionSourcesDrawer';
import { dataSourceCatalog } from './catalog';
import { type HealthFilter, healthCounts, needingAttention } from './ingestionSources';
import { HEALTH_LABEL } from './ingestionStatusFormat';
import { HEALTH_COLOR } from './ingestionTone';

interface Props {
  projectId: string;
  /** Outside Data sources: the empty state links there instead of "below". */
  onAddSource?: () => void;
}

const REFRESH_MS = 30_000;

const HEALTH_ORDER: IngestionHealth[] = ['sending', 'quiet', 'stopped'];

const catalogNames = new Map(dataSourceCatalog.map((d) => [d.id, d.name]));

interface WidgetProps {
  label: string;
  value: number;
  note: string;
  ariaLabel: string;
  onClick: () => void;
  dot?: string;
  valueColor?: string;
  children?: ReactNode;
}

/** One clickable count of the ingestion status tile. */
const Widget = ({
  label,
  value,
  note,
  ariaLabel,
  onClick,
  dot,
  valueColor,
  children,
}: WidgetProps): JSX.Element => (
  <button
    type="button"
    onClick={onClick}
    aria-label={ariaLabel}
    className="hover:border-accent-400 flex min-w-0 cursor-pointer flex-col gap-1.5 rounded-xl border border-gray-200 px-4 py-3 text-left transition-colors hover:bg-gray-50 dark:border-gray-700 dark:hover:bg-gray-700/40"
  >
    <span className="flex items-center gap-1.5 text-xs font-medium tracking-wide text-gray-500 uppercase dark:text-gray-400">
      {dot && <span className="inline-block h-2 w-2 rounded-full" style={{ background: dot }} />}
      {label}
    </span>
    <span
      className="text-2xl font-semibold text-gray-900 tabular-nums dark:text-gray-50"
      style={valueColor ? { color: valueColor } : undefined}
    >
      {value.toLocaleString()}
    </span>
    {children}
    <span className="truncate text-xs text-gray-500 dark:text-gray-400" title={note}>
      {note}
    </span>
  </button>
);

// Quiet and stopped name the most recently lost source, so the tile says
// which one without opening the list.
const healthNote = (
  health: IngestionHealth,
  status: IngestionStatus,
  attention: IngestionSource[],
): string => {
  if (health === 'sending') return `data in the last ${status.quietAfterMinutes} min`;
  const lost = attention.filter((source) => source.health === health);
  if (lost.length === 0)
    return health === 'quiet'
      ? `none silent for ${status.quietAfterMinutes} min`
      : `none silent for ${status.stoppedAfterHours} h`;
  return lost.length === 1 ? lost[0].name : `${lost[0].name} +${lost.length - 1} more`;
};

const typeName = (source: IngestionSource) =>
  source.dataSource ? catalogNames.get(source.dataSource) : undefined;

/**
 * What is sending to the project right now: one tile of counts (sources,
 * sending, quiet, stopped), each opening a filterable table of the sources.
 * Refreshes every 30 s while the tab is visible.
 */
export const IngestionStatusComponent = ({ projectId, onAddSource }: Props): JSX.Element => {
  const [status, setStatus] = useState<IngestionStatus | undefined>(undefined);
  const [error, setError] = useState<string | undefined>(undefined);
  const [isLoading, setIsLoading] = useState(true);
  const [now, setNow] = useState(() => Date.now());
  const [drawer, setDrawer] = useState<HealthFilter>();
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

  const sources = useMemo(() => status?.sources ?? [], [status]);
  const counts = useMemo(() => healthCounts(sources), [sources]);
  const attention = useMemo(() => needingAttention(sources), [sources]);
  const open = (health: HealthFilter) => setDrawer(health);

  return (
    <Card
      title="Ingestion status"
      subtitle={
        status
          ? `Sources sending to this project. Quiet: nothing for ${status.quietAfterMinutes} minutes; stopped: nothing for ${status.stoppedAfterHours} hours.`
          : 'Sources sending to this project and their health.'
      }
      extra={
        <>
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
          {onAddSource ? (
            <>
              No data yet.{' '}
              <button
                type="button"
                onClick={onAddSource}
                className="text-accent-600 dark:text-accent-400 cursor-pointer hover:underline"
              >
                Add a data source
              </button>
            </>
          ) : (
            'No data yet — pick a source below'
          )}
        </div>
      )}

      {sources.length > 0 && status && (
        <div className="grid grid-cols-2 gap-3 lg:grid-cols-4">
          <Widget
            label="Sources"
            value={sources.length}
            note={`${Math.round((counts.sending / sources.length) * 100)}% sending`}
            ariaLabel={`${sources.length} sources; show all`}
            onClick={() => open('all')}
          >
            <span
              className="flex h-1.5 overflow-hidden rounded-full bg-[var(--v2-track)]"
              aria-hidden
            >
              {HEALTH_ORDER.map((health) =>
                counts[health] > 0 ? (
                  <span
                    key={health}
                    style={{
                      width: `${(counts[health] / sources.length) * 100}%`,
                      background: HEALTH_COLOR[health],
                    }}
                  />
                ) : null,
              )}
            </span>
          </Widget>
          {HEALTH_ORDER.map((health) => (
            <Widget
              key={health}
              label={HEALTH_LABEL[health]}
              dot={HEALTH_COLOR[health]}
              value={counts[health]}
              valueColor={
                health !== 'sending' && counts[health] > 0 ? HEALTH_COLOR[health] : undefined
              }
              note={healthNote(health, status, attention)}
              ariaLabel={`${counts[health]} ${HEALTH_LABEL[health].toLowerCase()}; show them`}
              onClick={() => open(health)}
            />
          ))}
        </div>
      )}

      {status && drawer && (
        <IngestionSourcesDrawer
          status={status}
          now={now}
          initialHealth={drawer}
          typeName={typeName}
          onClose={() => setDrawer(undefined)}
        />
      )}
    </Card>
  );
};
