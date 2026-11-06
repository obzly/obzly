import { CheckCircleOutlined, ClockCircleOutlined, LoadingOutlined } from '@ant-design/icons';
import { Button, Table, Tag, Tooltip } from 'antd';
import { useCallback, useEffect, useState } from 'react';

import { APP_VERSION, IS_CONTROL_PLANE } from '../../../constants';
import { type SiteAdmin, sitesApi } from '../../../entity/sites';
import { type UpgradeStatus, type UpgradeStatusScope, systemApi } from '../../../entity/system';
import { UpgradeStatusComponent } from './UpgradeStatusComponent';

// Release builds are numbered; a local build says dev.
const displayVersion = /^\d/.test(APP_VERSION) ? `v${APP_VERSION}` : APP_VERSION;

interface Row {
  key: UpgradeStatusScope;
  name: string;
  role: string;
  status?: UpgradeStatus;
  error?: string;
}

const releaseUrl = () =>
  /^\d/.test(APP_VERSION)
    ? `https://github.com/obzly/obzly/releases/tag/v${APP_VERSION}`
    : 'https://github.com/obzly/obzly/releases';

export const UpdatesComponent = () => {
  const [rows, setRows] = useState<Row[]>();

  const load = useCallback(async () => {
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
    <div className="flex max-w-5xl flex-col gap-4">
      <div className="rounded-md border border-gray-200 p-4 text-sm dark:border-gray-700">
        <div className="font-medium text-gray-900 dark:text-gray-100">
          {IS_CONTROL_PLANE ? 'This control plane' : 'This deployment'} runs {displayVersion}
          {behind.length > 0 &&
            `; ${behind.length} deployment${behind.length === 1 ? '' : 's'} on another version`}
        </div>
        <div className="mt-1 text-gray-500 dark:text-gray-300">
          Upgrade Sites first or the control plane first: each runs its own migrations. Deploy the
          next version on a deployment only once it says it&apos;s ready.{' '}
          <a href={releaseUrl()} target="_blank" rel="noreferrer" className="!text-emerald-600">
            Release notes
          </a>
        </div>
      </div>

      <Table<Row>
        rowKey="key"
        size="middle"
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
                    <Tag color="red">Unreachable</Tag>
                  </Tooltip>
                );
              }
              if (!row.status) return '—';
              const { safeToUpgrade, blockers, warnings } = row.status.verdict;
              return (
                <span className="inline-flex flex-wrap items-center gap-2">
                  {safeToUpgrade ? (
                    <Tag icon={<CheckCircleOutlined />} color="green">
                      Ready for the next version
                    </Tag>
                  ) : (
                    <Tag icon={<ClockCircleOutlined />} color="orange">
                      Not yet
                    </Tag>
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
      <div>
        <Button size="small" onClick={() => void load()}>
          Check again
        </Button>
      </div>
    </div>
  );
};
