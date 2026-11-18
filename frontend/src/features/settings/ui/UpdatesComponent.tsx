import { CheckCircleOutlined, ClockCircleOutlined, LoadingOutlined } from '@ant-design/icons';
import { Button, Table, Tooltip } from 'antd';
import dayjs from 'dayjs';
import { useCallback, useEffect, useState } from 'react';

import { APP_VERSION, IS_CONTROL_PLANE } from '../../../constants';
import { type SiteAdmin, sitesApi } from '../../../entity/sites';
import { type LatestRelease, type UpgradeStatusScope, systemApi } from '../../../entity/system';
import { Card } from '../../../shared/ui/v2';
import { type Deployment, type StepState, upgradeOrder } from '../lib/upgradeOrder';
import { UpgradeStatusComponent } from './UpgradeStatusComponent';

const PILL = 'inline-flex items-center gap-1 rounded-full px-2 py-0.5 text-xs font-medium';

// Release builds are numbered; a local build says dev.
const displayVersion = /^\d/.test(APP_VERSION) ? `v${APP_VERSION}` : APP_VERSION;

interface Row extends Deployment {
  key: UpgradeStatusScope;
}

const STEP_PILL: Record<StepState, { label: string; cls: string }> = {
  current: { label: 'Up to date', cls: 'bg-[var(--v2-ok-soft)] text-[var(--v2-accent)]' },
  ready: { label: 'Upgrade', cls: 'bg-[var(--v2-ok-soft)] text-[var(--v2-accent)]' },
  waiting: { label: 'Not yet', cls: 'bg-orange-500/15 text-[var(--v2-orange)]' },
  'after-sites': {
    label: 'After the Sites',
    cls: 'bg-gray-100 text-gray-600 dark:bg-gray-700 dark:text-gray-300',
  },
  unknown: { label: 'n/a', cls: 'bg-gray-100 text-gray-500 dark:bg-gray-700 dark:text-gray-400' },
};

const releaseUrl = () =>
  /^\d/.test(APP_VERSION)
    ? `https://github.com/obzly/obzly/releases/tag/v${APP_VERSION}`
    : 'https://github.com/obzly/obzly/releases';

export const UpdatesComponent = () => {
  const [rows, setRows] = useState<Row[]>();
  const [latest, setLatest] = useState<LatestRelease>();

  const load = useCallback(async (refresh = false) => {
    // A failed check is unknown, never "up to date".
    void systemApi
      .getLatestRelease(refresh)
      .then(setLatest)
      .catch((error: Error) => setLatest({ status: 'unknown', error: error.message }));
    const deployments: Row[] = IS_CONTROL_PLANE
      ? [
          { key: 'central', name: 'Control plane', role: 'Control plane' },
          ...(await sitesApi.listSitesAdmin().catch(() => [] as SiteAdmin[]))
            .filter((site) => site.enabled)
            .map((site): Row => ({
              key: `site:${site.id}`,
              name: site.displayName || site.id,
              role: 'Site',
            })),
        ]
      : [{ key: 'site', name: 'This deployment', role: 'Site' }];
    setRows(
      await Promise.all(
        deployments.map(async (row) => {
          try {
            return { ...row, status: await systemApi.getUpgradeStatus(row.key) };
          } catch (error) {
            return { ...row, error: (error as Error).message };
          }
        }),
      ),
    );
  }, []);

  useEffect(() => {
    void load();
  }, [load]);

  const behind = (rows ?? []).filter(
    (row) => row.status && row.status.version !== APP_VERSION && /^\d/.test(APP_VERSION),
  );

  return (
    <div className="flex flex-col gap-4">
      <Card
        title="Updates"
        subtitle={`${IS_CONTROL_PLANE ? 'This control plane' : 'This deployment'} runs ${displayVersion}${
          behind.length > 0
            ? `; ${behind.length} deployment${behind.length === 1 ? '' : 's'} on another version`
            : ''
        }`}
        extra={
          <Button size="small" onClick={() => void load(true)}>
            Check again
          </Button>
        }
        bodyClassName="flex flex-col gap-3"
      >
        <div className="rounded-xl bg-gray-50 px-3.5 py-3 text-[13.5px] text-gray-600 dark:bg-gray-900/40 dark:text-gray-300">
          Upgrade each Site first and the control plane last: the control plane ships the UI and
          must not be more than one version ahead of the Sites. Each runs its own migrations; deploy
          the next version on a deployment only once it says it&apos;s ready.{' '}
          <a href={releaseUrl()} target="_blank" rel="noreferrer" className="!text-accent-600">
            Release notes
          </a>
        </div>
        <Table<Row>
          rowKey="key"
          size="small"
          loading={!rows && { indicator: <LoadingOutlined spin /> }}
          dataSource={rows ?? []}
          pagination={false}
          scroll={{ x: 760 }}
          expandable={{
            expandedRowRender: (row) => (
              <UpgradeStatusComponent scope={row.key} title={`${row.role} ${row.name}`} />
            ),
          }}
          columns={[
            { title: 'Deployment', dataIndex: 'name', render: (name: string) => <b>{name}</b> },
            { title: 'Role', dataIndex: 'role', width: 130 },
            {
              title: 'Version',
              key: 'version',
              width: 130,
              render: (_, row) =>
                row.status ? <span className="font-mono text-xs">{row.status.version}</span> : '—',
            },
            {
              title: 'Readiness',
              key: 'ready',
              render: (_, row) => {
                if (row.error) {
                  return (
                    <Tooltip title={row.error}>
                      <span className={`${PILL} bg-red-500/15 text-[var(--v2-red)]`}>
                        Unreachable
                      </span>
                    </Tooltip>
                  );
                }
                if (!row.status) return '—';
                const { safeToUpgrade, blockers, warnings } = row.status.verdict;
                return (
                  <span className="inline-flex flex-wrap items-center gap-2">
                    {safeToUpgrade ? (
                      <span className={`${PILL} bg-[var(--v2-ok-soft)] text-[var(--v2-accent)]`}>
                        <CheckCircleOutlined /> Ready for the next version
                      </span>
                    ) : (
                      <span className={`${PILL} bg-orange-500/15 text-[var(--v2-orange)]`}>
                        <ClockCircleOutlined /> Not yet
                      </span>
                    )}
                    <span className="text-xs text-gray-500 dark:text-gray-400">
                      {[...blockers, ...warnings].slice(0, 2).join('; ')}
                    </span>
                  </span>
                );
              },
            },
          ]}
        />
      </Card>
      <UpgradeOrderCard rows={rows} latest={latest} />
    </div>
  );
};

const UpgradeOrderCard = ({ rows, latest }: { rows?: Row[]; latest?: LatestRelease }) => {
  const steps = upgradeOrder(rows ?? [], latest);
  const behind = steps.filter((step) => step.state !== 'current' && step.state !== 'unknown');
  const header = (() => {
    if (!latest) return null;
    if (latest.status === 'disabled') {
      return {
        text: 'Update check disabled',
        cls: 'bg-gray-100 text-gray-600 dark:bg-gray-700 dark:text-gray-300',
      };
    }
    if (latest.status !== 'known' || !latest.version) {
      return {
        text: 'Latest release unknown',
        cls: 'bg-gray-100 text-gray-600 dark:bg-gray-700 dark:text-gray-300',
      };
    }
    return behind.length > 0
      ? {
          text: `v${latest.version} available`,
          cls: 'bg-[var(--v2-ok-soft)] text-[var(--v2-accent)]',
        }
      : {
          text: `v${latest.version} is the latest`,
          cls: 'bg-gray-100 text-gray-600 dark:bg-gray-700 dark:text-gray-300',
        };
  })();

  return (
    <Card
      title="Upgrade order"
      subtitle={IS_CONTROL_PLANE ? 'Sites first, then the control plane' : 'This deployment'}
      extra={header && <span className={`${PILL} ${header.cls}`}>{header.text}</span>}
      bodyClassName="flex flex-col"
    >
      {!latest && <LoadingOutlined spin />}
      {latest?.status === 'disabled' && (
        <p className="m-0 text-sm text-gray-500 dark:text-gray-400">
          The update check is off (UPDATE_CHECK=false), so which deployments are behind isn&apos;t
          known. See the{' '}
          <a href={releaseUrl()} target="_blank" rel="noreferrer" className="!text-accent-600">
            releases
          </a>
          .
        </p>
      )}
      {latest && latest.status === 'unknown' && (
        <p className="m-0 text-sm text-gray-500 dark:text-gray-400">
          Couldn&apos;t read the latest release{latest.error ? ` (${latest.error})` : ''}, so which
          deployments are behind isn&apos;t known.
        </p>
      )}
      {latest?.status === 'known' && (
        <>
          <p className="m-0 mb-1 text-sm text-gray-500 dark:text-gray-400">
            Latest release v{latest.version}
            {latest.publishedAt && `, published ${dayjs(latest.publishedAt).format('MMM D, YYYY')}`}
            {latest.url && (
              <>
                {' · '}
                <a href={latest.url} target="_blank" rel="noreferrer" className="!text-accent-600">
                  Release notes
                </a>
              </>
            )}
          </p>
          {!rows && <LoadingOutlined spin />}
          {steps.map((step) => (
            <div
              key={step.key}
              className="grid grid-cols-[minmax(120px,1fr)_minmax(140px,2fr)_auto] items-center gap-3.5 border-t border-gray-200 py-2.5 dark:border-gray-700"
            >
              <span className="flex min-w-0 flex-col">
                <b className="truncate">{step.name}</b>
                <span className="font-mono text-xs text-gray-500 dark:text-gray-400">
                  {step.version
                    ? step.state === 'current' || step.state === 'unknown'
                      ? step.version
                      : `${step.version} → ${latest.version}`
                    : 'n/a'}
                </span>
              </span>
              <span className="text-[13px] text-gray-600 dark:text-gray-300">{step.note}</span>
              <span className={`${PILL} ${STEP_PILL[step.state].cls}`}>
                {STEP_PILL[step.state].label}
              </span>
            </div>
          ))}
        </>
      )}
    </Card>
  );
};
