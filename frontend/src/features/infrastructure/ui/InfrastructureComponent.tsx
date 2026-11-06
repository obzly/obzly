// A control plane reads every Site it can reach; a standalone Site shows its own.
import { LoadingOutlined, ReloadOutlined } from '@ant-design/icons';
import { Alert, Button, Spin, Tabs, Tooltip } from 'antd';
import { useState } from 'react';

import type { AssetKind, SiteAsset } from '../../../entity/inventory';
import { useVisibleInterval } from '../../../shared/lib';
import { useInventory } from '../lib/useInventory';
import { AssetDetailDrawer, type NavigateTarget } from './AssetDetailDrawer';
import { AssetsTableComponent } from './AssetsTableComponent';
import { CloudConnectionsComponent } from './CloudConnectionsComponent';
import { OverviewComponent } from './OverviewComponent';

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

  useVisibleInterval(() => setRefresh((value) => value + 1), REFRESH_MS);

  const openAsset = (asset: SiteAsset) =>
    setOpen({ siteId: asset.siteId, siteName: asset.siteName, id: asset.id });
  const failed = data.sites.filter((site) => site.error);
  const truncated = data.sites.filter((site) => site.truncated);

  return (
    <div
      className="w-full overflow-y-auto rounded bg-white p-5 shadow dark:bg-gray-800"
      style={{ height: contentHeight }}
    >
      <div className="mb-1 flex flex-wrap items-center gap-3">
        <h1 className="m-0 text-xl font-semibold text-gray-900 dark:text-gray-100">
          Infrastructure
        </h1>
        <span className="text-sm text-gray-500 dark:text-gray-400">
          {multiSite ? 'All Sites you can reach' : 'This Site'}
        </span>
        {multiSite &&
          data.sites.map((site) => (
            <Tooltip key={site.siteId} title={site.error ?? `${site.assets} assets`}>
              <span
                className={`inline-flex items-center gap-1.5 rounded-full border px-2.5 py-0.5 text-xs ${site.error ? 'border-red-300 bg-red-50 text-red-600 dark:border-red-800 dark:bg-red-950 dark:text-red-400' : 'border-gray-200 text-gray-600 dark:border-gray-600 dark:text-gray-300'}`}
              >
                <span
                  className={`inline-block h-2 w-2 rounded-full ${site.error ? 'bg-red-500' : 'bg-emerald-500'}`}
                />
                {site.siteName}
                {site.error && ': unavailable'}
              </span>
            </Tooltip>
          ))}
        <div className="flex-1" />
        {view !== 'cloud' && (
          <Button
            icon={<ReloadOutlined />}
            loading={data.loading && refresh > 0}
            onClick={() => setRefresh((value) => value + 1)}
          >
            Refresh
          </Button>
        )}
      </div>

      {data.error && <Alert className="my-3" type="error" showIcon title={data.error} />}
      {failed.length > 0 && !multiSite && (
        <Alert className="my-3" type="error" showIcon title={failed[0].error} />
      )}
      {truncated.length > 0 && (
        <Alert
          className="my-3"
          type="info"
          showIcon
          title={`Showing the first 5000 assets of ${truncated.map((site) => `${site.siteName || 'this Site'} (${site.assets})`).join(', ')}, worst health first.`}
        />
      )}

      <Tabs
        activeKey={view}
        onChange={(key) => setView(key as View)}
        items={[
          { key: 'overview', label: 'Overview' },
          {
            key: 'assets',
            label: (
              <span>
                Assets{' '}
                <span className="text-xs text-gray-500 dark:text-gray-400">
                  {data.assets.length || ''}
                </span>
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
              assets={data.assets}
              changes={data.changes}
              multiSite={multiSite}
              onOpen={openAsset}
              onOpenKind={(kind) => {
                setKinds(new Set([kind]));
                setView('assets');
              }}
            />
          )}
          {view === 'assets' && (
            <AssetsTableComponent
              assets={data.assets}
              sites={data.sites}
              multiSite={multiSite}
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
