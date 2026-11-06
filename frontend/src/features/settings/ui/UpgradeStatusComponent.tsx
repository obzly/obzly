// Upgrade readiness. The ETA comes from catalog counts between polls, so it
// holds even when the rewrite worker runs on another replica.
import {
  CheckCircleOutlined,
  ClockCircleOutlined,
  LoadingOutlined,
  ReloadOutlined,
} from '@ant-design/icons';
import { Alert, Button, Progress, Spin, Table, Tag, Tooltip } from 'antd';
import type { ColumnsType } from 'antd/es/table';
import dayjs from 'dayjs';
import type { JSX, ReactNode } from 'react';
import { useEffect, useRef, useState } from 'react';

import {
  type IngestStreamStatus,
  type SchemaState,
  type StreamBacklog,
  type UpgradeStatus,
  type UpgradeStatusScope,
  systemApi,
} from '../../../entity/system';

const REFRESH_INTERVAL_MS = 10_000;
// Polls the rate is measured over; fewer lets one slow batch skew it.
const RATE_WINDOW = 6;
// Without progress for this many polls, the worker probably isn't running.
const STALL_POLLS = 3;

interface Props {
  scope: UpgradeStatusScope;
  title: string;
}

interface Sample {
  at: number;
  remaining: number;
}

const SCHEMA_TAG: Record<SchemaState, { color: string; label: string }> = {
  'up-to-date': { color: 'green', label: 'Up to date' },
  pending: { color: 'orange', label: 'Migrations pending' },
  ahead: { color: 'red', label: 'Newer than this build' },
  unknown: { color: 'default', label: 'Unknown' },
};

const formatDuration = (seconds: number): string => {
  if (seconds < 60) return 'under a minute';
  if (seconds < 3600) return `about ${Math.ceil(seconds / 60)} min`;
  const hours = seconds / 3600;
  return `about ${hours < 10 ? hours.toFixed(1) : Math.round(hours)} h`;
};

// Segments/s over the sample window; undefined until measurable.
const rateOf = (samples: Sample[]): number | undefined => {
  if (samples.length < 2) return undefined;
  const first = samples[0];
  const last = samples[samples.length - 1];
  const seconds = (last.at - first.at) / 1000;
  const done = first.remaining - last.remaining;
  return seconds > 0 && done > 0 ? done / seconds : undefined;
};

const Card = ({ title, children }: { title: ReactNode; children: ReactNode }) => (
  <div className="rounded-md border border-gray-200 p-4 text-sm dark:border-gray-700">
    <div className="mb-3 font-medium text-gray-900 dark:text-gray-100">{title}</div>
    {children}
  </div>
);

const Row = ({ label, children }: { label: string; children: ReactNode }) => (
  <>
    <dt className="text-gray-500 dark:text-gray-300">{label}</dt>
    <dd className="text-gray-900 dark:text-gray-100">{children}</dd>
  </>
);

const formatBytes = (bytes: number) =>
  bytes >= 1 << 30
    ? `${(bytes / (1 << 30)).toFixed(1)} GiB`
    : bytes >= 1 << 20
      ? `${(bytes / (1 << 20)).toFixed(1)} MiB`
      : `${Math.round(bytes / 1024)} KiB`;

const ingestColumns: ColumnsType<IngestStreamStatus> = [
  { title: 'Stream', dataIndex: 'stream' },
  { title: 'Batches', dataIndex: 'messages', align: 'right' },
  {
    title: 'Size',
    key: 'bytes',
    align: 'right',
    render: (_, row) =>
      row.maxBytes > 0
        ? `${formatBytes(row.bytes)} of ${formatBytes(row.maxBytes)}`
        : formatBytes(row.bytes),
  },
  {
    title: 'Oldest',
    key: 'oldest',
    align: 'right',
    render: (_, row) => (row.oldestAt ? dayjs(row.oldestAt).fromNow(true) : '-'),
  },
  {
    title: (
      <Tooltip title="Broker replicas offline or behind the stream leader.">
        Replicas behind
      </Tooltip>
    ),
    dataIndex: 'replicasLagging',
    align: 'right',
  },
];

const backlogColumns = (objectStorage: boolean): ColumnsType<StreamBacklog> => [
  { title: 'Stream', dataIndex: 'stream' },
  { title: 'Segments', dataIndex: 'total', align: 'right' },
  {
    title: (
      <Tooltip title="Below a quarter of the target segment size; the compactor merges these on its 5-minute pass. Informational.">
        Awaiting compaction
      </Tooltip>
    ),
    dataIndex: 'smallSegments',
    align: 'right',
  },
  ...(objectStorage
    ? [
        {
          title: (
            <Tooltip title="Only on this replica's disk until the uploader pushes them to object storage.">
              Awaiting upload
            </Tooltip>
          ),
          key: 'pendingUpload',
          align: 'right' as const,
          render: (_: unknown, row: StreamBacklog) =>
            row.pendingUpload ? (
              <Tooltip
                title={
                  row.oldestPendingUploadAt
                    ? `Oldest waiting since ${dayjs(row.oldestPendingUploadAt).fromNow()}`
                    : undefined
                }
              >
                <span className="text-amber-600 dark:text-amber-400">{row.pendingUpload}</span>
              </Tooltip>
            ) : (
              0
            ),
        },
      ]
    : []),
];

export const UpgradeStatusComponent = ({ scope, title }: Props): JSX.Element => {
  const [status, setStatus] = useState<UpgradeStatus | undefined>(undefined);
  const [error, setError] = useState<string | undefined>(undefined);
  const [isLoading, setIsLoading] = useState(true);
  const [samples, setSamples] = useState<Sample[]>([]);
  const timerRef = useRef<number | undefined>(undefined);

  const load = async () => {
    try {
      const next = await systemApi.getUpgradeStatus(scope);
      setStatus(next);
      setError(undefined);
      if (next.rewrites) {
        const remaining = next.rewrites.remaining;
        setSamples((prev) => [...prev, { at: Date.now(), remaining }].slice(-RATE_WINDOW));
      }
    } catch (e) {
      setError((e as Error).message);
    }
    setIsLoading(false);
  };

  useEffect(() => {
    setStatus(undefined);
    setSamples([]);
    setIsLoading(true);
    load();
    timerRef.current = window.setInterval(load, REFRESH_INTERVAL_MS);
    return () => window.clearInterval(timerRef.current);
  }, [scope]);

  const rewrites = status?.rewrites;
  const rate = rateOf(samples);
  const remaining = rewrites?.remaining ?? 0;
  const eta = rate && remaining > 0 ? formatDuration(remaining / rate) : undefined;
  const stalled =
    remaining > 0 &&
    samples.length >= STALL_POLLS &&
    samples.slice(-STALL_POLLS).every((s) => s.remaining === remaining);

  if (isLoading) {
    return (
      <Card title={title}>
        <Spin indicator={<LoadingOutlined spin />} />
      </Card>
    );
  }

  if (!status) {
    return (
      <Card title={title}>
        <Alert
          type="error"
          showIcon
          message="Couldn't read the upgrade status"
          description={error}
          action={
            <Button size="small" icon={<ReloadOutlined />} onClick={load}>
              Retry
            </Button>
          }
        />
      </Card>
    );
  }

  const { schema, verdict } = status;
  const schemaTag = SCHEMA_TAG[schema.state];
  const lastApplied = schema.recent[0];

  return (
    <Card
      title={
        <div className="flex flex-wrap items-center gap-2">
          <span>{title}</span>
          <Tag className="!m-0 font-mono">{status.version}</Tag>
          <Tag className="!m-0">{status.serviceMode}</Tag>
          <span className="ml-auto flex items-center gap-2 text-xs font-normal text-gray-500 dark:text-gray-400">
            {error && <span className="text-amber-600 dark:text-amber-400">Refresh failed</span>}
            Checked {dayjs(status.checkedAt).format('HH:mm:ss')}
            <Button size="small" type="text" icon={<ReloadOutlined />} onClick={load} />
          </span>
        </div>
      }
    >
      {verdict.safeToUpgrade ? (
        <Alert
          type="success"
          showIcon
          icon={<CheckCircleOutlined />}
          message="Safe to deploy the next version"
          description="The schema is current and no background work from this version is outstanding."
        />
      ) : (
        <Alert
          type="warning"
          showIcon
          icon={<ClockCircleOutlined />}
          message={
            eta
              ? `Wait ${eta} before deploying the next version`
              : 'Wait before deploying the next version'
          }
          description={
            <ul className="mb-0 list-disc pl-4">
              {verdict.blockers.map((blocker) => (
                <li key={blocker}>{blocker}</li>
              ))}
            </ul>
          }
        />
      )}
      {stalled && (
        <Alert
          className="mt-2"
          type="error"
          showIcon
          message="No rewrite progress in the last 30 seconds"
          description={
            rewrites?.workerOnThisReplica
              ? 'The worker on this replica may be stuck - check its logs.'
              : 'The rewrite worker runs on a worker replica. Check that one is running and holds the lease.'
          }
        />
      )}
      {verdict.warnings.length > 0 && (
        <Alert
          className="mt-2"
          type="info"
          showIcon
          message={
            <ul className="mb-0 list-disc pl-4">
              {verdict.warnings.map((warning) => (
                <li key={warning}>{warning}</li>
              ))}
            </ul>
          }
        />
      )}

      <div className="mt-4 grid grid-cols-1 gap-4 lg:grid-cols-2">
        <div>
          <div className="mb-2 font-medium text-gray-800 dark:text-gray-200">Database schema</div>
          <dl className="grid grid-cols-[max-content_1fr] gap-x-4 gap-y-1.5">
            <Row label="State">
              <Tag color={schemaTag.color} className="!m-0">
                {schemaTag.label}
              </Tag>
            </Row>
            <Row label="Applied">
              <span className="font-mono">{schema.currentVersion || '-'}</span>
              {schema.latestAvailable > 0 && schema.latestAvailable !== schema.currentVersion && (
                <span className="ml-2 text-gray-500 dark:text-gray-400">
                  this build ships <span className="font-mono">{schema.latestAvailable}</span>
                </span>
              )}
            </Row>
            <Row label="Migrations">{schema.appliedCount}</Row>
            {schema.releasesAhead > 0 && (
              <Row label="Releases ahead">
                <Tooltip title="Releases that recorded a newer schema than this build ships. One is a rollout in progress or a supported rollback; more is past the one-version boundary.">
                  {schema.releasesAhead}
                </Tooltip>
              </Row>
            )}
            {lastApplied && (
              <Row label="Last applied">
                <Tooltip title={dayjs(lastApplied.appliedAt).format('YYYY-MM-DD HH:mm:ss')}>
                  {dayjs(lastApplied.appliedAt).fromNow()}
                </Tooltip>
              </Row>
            )}
          </dl>
        </div>

        {rewrites && (
          <div>
            <div className="mb-2 font-medium text-gray-800 dark:text-gray-200">
              Segment rewrites
              {remaining > 0 && rate !== undefined && (
                <span className="ml-2 font-normal text-gray-500 dark:text-gray-400">
                  {rate < 1 ? rate.toFixed(2) : Math.round(rate)} segments/s
                </span>
              )}
            </div>
            <div className="space-y-2">
              {rewrites.streams.map((stream) => {
                const done = stream.total - stream.legacy;
                const percent = stream.total ? Math.floor((done / stream.total) * 100) : 100;
                return (
                  <div key={stream.stream}>
                    <div className="mb-0.5 flex justify-between text-xs">
                      <span className="text-gray-600 dark:text-gray-300">{stream.stream}</span>
                      <span className="text-gray-900 dark:text-gray-100">
                        {stream.legacy === 0
                          ? `${stream.total} current`
                          : `${done} / ${stream.total}`}
                        {stream.skipped > 0 && (
                          <span className="ml-1 text-amber-600 dark:text-amber-400">
                            ({stream.skipped} skipped)
                          </span>
                        )}
                      </span>
                    </div>
                    <Progress
                      percent={percent}
                      showInfo={false}
                      size="small"
                      strokeColor={stream.legacy === 0 ? '#059669' : '#d97706'}
                    />
                  </div>
                );
              })}
            </div>
            {rewrites.completed.length > 0 && (
              <div className="mt-3 text-xs text-gray-500 dark:text-gray-400">
                Completed:{' '}
                {rewrites.completed
                  .map((m) => `${m.name} (${dayjs(m.completedAt).format('YYYY-MM-DD')})`)
                  .join(', ')}
              </div>
            )}
          </div>
        )}
      </div>

      {status.segments && (
        <div className="mt-4">
          <div className="mb-2 font-medium text-gray-800 dark:text-gray-200">
            Segment backlog
            {!status.objectStorageEnabled && (
              <span className="ml-2 font-normal text-gray-500 dark:text-gray-400">
                object storage off, local disk only
              </span>
            )}
          </div>
          <Table<StreamBacklog>
            size="small"
            rowKey="stream"
            pagination={false}
            columns={backlogColumns(status.objectStorageEnabled)}
            dataSource={status.segments}
          />
        </div>
      )}

      {status.integrityAudit && (
        <div className="mt-4">
          <div className="mb-2 font-medium text-gray-800 dark:text-gray-200">
            Integrity audit
            <span className="ml-2 font-normal text-gray-500 dark:text-gray-400">
              uploaded segments checked against object storage
            </span>
          </div>
          <div className="text-sm text-gray-700 dark:text-gray-200">
            <Tooltip title={dayjs(status.integrityAudit.finishedAt).format('YYYY-MM-DD HH:mm:ss')}>
              {dayjs(status.integrityAudit.finishedAt).fromNow()}
            </Tooltip>
            : {status.integrityAudit.sampled} sampled,{' '}
            {status.integrityAudit.failed === 0 ? (
              <span className="text-emerald-600 dark:text-emerald-400">all intact</span>
            ) : (
              <span className="text-red-600 dark:text-red-400">
                {status.integrityAudit.failed} failed
              </span>
            )}
          </div>
          {status.integrityAudit.failures.length > 0 && (
            <ul className="mt-1 list-disc pl-5 text-xs text-gray-600 dark:text-gray-300">
              {status.integrityAudit.failures.slice(0, 10).map((failure) => (
                <li key={failure.segmentId}>
                  <span className="font-mono">{failure.objectKey}</span>: {failure.problem}
                  {failure.detail ? ` (${failure.detail})` : ''}
                </li>
              ))}
            </ul>
          )}
        </div>
      )}

      {status.ingestBuffer && status.ingestBuffer.length > 0 && (
        <div className="mt-4">
          <div className="mb-2 font-medium text-gray-800 dark:text-gray-200">
            Ingest buffer
            <span className="ml-2 font-normal text-gray-500 dark:text-gray-400">
              acknowledged batches waiting for a worker
            </span>
          </div>
          <Table<IngestStreamStatus>
            size="small"
            rowKey="stream"
            pagination={false}
            columns={ingestColumns}
            dataSource={status.ingestBuffer}
          />
        </div>
      )}
    </Card>
  );
};
