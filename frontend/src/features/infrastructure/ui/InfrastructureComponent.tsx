// A control plane reads every Site it can reach; a standalone Site shows its own.
import { LoadingOutlined, ReloadOutlined } from '@ant-design/icons';
import { Alert, Button, Spin, Tabs, Tooltip } from 'antd';
import { useMemo, useState } from 'react';

import type { AssetKind } from '../../../entity/inventory';
import { useVisibleInterval } from '../../../shared/lib';
import { PageHeader } from '../../../shared/ui/v2';
import { mergeSummaries } from '../lib/paging';
import { useInventory } from '../lib/useInventory';
import { AssetDetailDrawer, type NavigateTarget } from './AssetDetailDrawer';
import { AssetsTableComponent } from './AssetsTableComponent';
import { CloudConnectionsComponent } from './CloudConnectionsComponent';
import { type AssetTarget, OverviewComponent } from './OverviewComponent';

interface Props {
  contentHeight: number;
  /** Several Sites: the control plane's view across them. */
  multiSite: boolean;
  /** Cloud connections are read-only here; admins manage them in settings. */
  onManageCloud?: () => void;
  onNavigate: (target: NavigateTarget) => void;
}

type View = 'overview' | 'assets' | 'cloud';

const REFRESH_MS = 60_000;

export const InfrastructureComponent = ({
  contentHeight,
  multiSite,
  onManageCloud,
  onNavigate,
}: Props) => {
  const [view, setView] = useState<View>('overview');
  const [refresh, setRefresh] = useState(0);
  const [kinds, setKinds] = useState<Set<AssetKind>>(new Set());
  const [open, setOpen] = useState<{ siteId: string; siteName: string; id: string }>();
  const data = useInventory(refresh);
  const summary = useMemo(
    () => mergeSummaries(data.summaries, multiSite),
    [data.summaries, multiSite],
  );

  useVisibleInterval(() => setRefresh((value) => value + 1), REFRESH_MS);

  const openAsset = (asset: AssetTarget) =>
    setOpen({ siteId: asset.siteId, siteName: asset.siteName, id: asset.id });
  const failed = data.sites.filter((site) => site.error);
  const terminated = summary.terminated;
  // The Assets tab lists live assets unless asked for terminated ones; its count does too.
  const live = summary.total - terminated;

  return (
    <div
      className="w-full overflow-y-auto rounded-2xl border border-gray-200 bg-white p-6 dark:border-gray-700 dark:bg-gray-800"
      style={{ height: contentHeight }}
    >
      <PageHeader
        title="Infrastructure"
        subtitle={multiSite ? 'All Sites you can reach' : 'This Site'}
        actions={
          <>
            {multiSite &&
              data.sites.map((site) => (
                <Tooltip key={site.siteId} title={site.error ?? `${site.assets} assets`}>
                  <span
                    className={`inline-flex items-center gap-1.5 rounded-lg border px-2.5 py-1 text-xs ${site.error ? 'border-red-300 bg-red-50 text-red-600 dark:border-red-800 dark:bg-red-950 dark:text-red-400' : 'border-gray-200 bg-gray-50 text-gray-600 dark:border-gray-700 dark:bg-gray-900 dark:text-gray-300'}`}
                  >
                    <span
                      className={`inline-block h-2 w-2 rounded-full ${site.error ? 'bg-red-500' : 'bg-emerald-500'}`}
                    />
                    {site.siteName}
                    {site.error && ': unavailable'}
                  </span>
                </Tooltip>
              ))}
            {view !== 'cloud' && (
              <Button
                icon={<ReloadOutlined />}
                loading={data.loading && refresh > 0}
                onClick={() => setRefresh((value) => value + 1)}
              >
                Refresh
              </Button>
            )}
          </>
        }
      />

      {data.error && <Alert className="mt-4" type="error" showIcon title={data.error} />}
      {failed.length > 0 && !multiSite && (
        <Alert className="mt-4" type="error" showIcon title={failed[0].error} />
      )}

      <Tabs
        className="mt-3"
        activeKey={view}
        onChange={(key) => setView(key as View)}
        items={[
          { key: 'overview', label: 'Overview' },
          {
            key: 'assets',
            label: (
              <span className="inline-flex items-center gap-1.5">
                Assets
                {live > 0 && (
                  <span className="rounded-full bg-gray-100 px-2 py-px text-xs text-gray-500 tabular-nums dark:bg-gray-700 dark:text-gray-300">
                    {live.toLocaleString()}
                  </span>
                )}
              </span>
            ),
          },
          // Cloud connections are kept on the control plane.
          ...(multiSite ? [{ key: 'cloud', label: 'Cloud connections' }] : []),
        ]}
      />

      {view === 'cloud' ? (
        <CloudConnectionsComponent canManage={false} onManage={onManageCloud} />
      ) : data.loading && refresh === 0 ? (
        <div className="flex justify-center py-16">
          <Spin indicator={<LoadingOutlined spin />} />
        </div>
      ) : (
        <>
          {view === 'overview' && (
            <OverviewComponent
              summary={summary}
              sites={data.summaries}
              changes={data.changes}
              history={data.history}
              multiSite={multiSite}
              refresh={refresh}
              onOpen={openAsset}
              onOpenKind={(kind) => {
                setKinds(new Set([kind]));
                setView('assets');
              }}
            />
          )}
          {view === 'assets' && (
            <AssetsTableComponent
              sites={data.sites}
              clusters={summary.clusters}
              inventoryTotal={summary.total}
              terminatedTotal={terminated}
              multiSite={multiSite}
              refresh={refresh}
              kinds={kinds}
              onKindsChange={setKinds}
              onOpen={openAsset}
            />
          )}
          <p className="mt-4 text-xs text-gray-500 dark:text-gray-400">
            Built every 5 minutes on each Site from the data its agents already send. Kubernetes pod
            phase, restarts and replica counts need the cluster agent from the latest Kubernetes
            data source manifest.
          </p>
        </>
      )}

      {open && (
        <AssetDetailDrawer
          siteId={open.siteId}
          siteName={open.siteName}
          assetId={open.id}
          multiSite={multiSite}
          onClose={() => setOpen(undefined)}
          onOpenAsset={(siteId, siteName, id) => setOpen({ siteId, siteName, id })}
          onNavigate={(target) => {
            setOpen(undefined);
            onNavigate(target);
          }}
        />
      )}
    </div>
  );
};
