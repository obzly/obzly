// Cost lives on each Site; a control plane reads every Site it can reach and adds the answers up
// per currency.
import { LoadingOutlined, ReloadOutlined } from '@ant-design/icons';
import { Alert, Button, Spin, Tabs } from 'antd';
import { useEffect, useState } from 'react';

import { useVisibleInterval } from '../../../shared/lib';
import { DEFAULT_EXPLORER, type ExplorerState, setFilterValues } from '../lib/explorer';
import type { CostSite } from '../lib/merge';
import { fitGranularity, resolveRange } from '../lib/ranges';
import { listCostSites } from '../lib/sites';
import { BudgetsTab } from './BudgetsTab';
import { ExplorerTab } from './ExplorerTab';
import { OverviewTab } from './OverviewTab';
import { RecommendationsTab } from './RecommendationsTab';

interface Props {
  contentHeight: number;
  /** Admins turn Cost on for a cloud connection in settings. */
  onManageCloud?: () => void;
}

type View = 'overview' | 'explorer' | 'recommendations' | 'budgets';

// Cost changes a few times a day; this only picks up a delivery while the tab stays open.
const REFRESH_MS = 15 * 60_000;

export const CostComponent = ({ contentHeight, onManageCloud }: Props) => {
  const [view, setView] = useState<View>('overview');
  const [sites, setSites] = useState<CostSite[]>();
  const [error, setError] = useState<string>();
  const [refresh, setRefresh] = useState(0);
  const [explorer, setExplorer] = useState<ExplorerState>(DEFAULT_EXPLORER);
  // The default saved view opens the Explorer only until it was set some other way.
  const [explorerSet, setExplorerSet] = useState(false);

  useEffect(() => {
    listCostSites()
      .then(setSites)
      .catch((reason: Error) => setError(reason.message));
  }, []);

  useVisibleInterval(() => setRefresh((value) => value + 1), REFRESH_MS);

  return (
    <div
      className="w-full overflow-y-auto rounded bg-white p-5 shadow dark:bg-gray-800"
      style={{ height: contentHeight }}
    >
      <div className="mb-1 flex flex-wrap items-center gap-3">
        <h1 className="m-0 text-xl font-semibold text-gray-900 dark:text-gray-100">Cost</h1>
        <span className="text-sm text-gray-500 dark:text-gray-400">
          {(sites?.length ?? 0) > 1
            ? 'All Sites you can reach'
            : 'Cloud spend from your connections'}
        </span>
        <div className="flex-1" />
        <Button icon={<ReloadOutlined />} onClick={() => setRefresh((value) => value + 1)}>
          Refresh
        </Button>
      </div>

      {error && <Alert className="my-3" type="error" showIcon title={error} />}

      <Tabs
        activeKey={view}
        onChange={(key) => setView(key as View)}
        items={[
          { key: 'overview', label: 'Overview' },
          { key: 'explorer', label: 'Explorer' },
          { key: 'recommendations', label: 'Recommendations' },
          { key: 'budgets', label: 'Budgets & anomalies' },
        ]}
      />

      {!sites ? (
        !error && (
          <div className="flex justify-center py-16">
            <Spin indicator={<LoadingOutlined spin />} />
          </div>
        )
      ) : (
        <>
          {view === 'overview' && (
            <OverviewTab
              sites={sites}
              refresh={refresh}
              onManageCloud={onManageCloud}
              onExplore={(groupBy, filter) => {
                const range = resolveRange('mtd', new Date().toISOString().slice(0, 10));
                setExplorer({
                  ...DEFAULT_EXPLORER,
                  range: 'mtd',
                  granularity: fitGranularity(range),
                  groupBy,
                  filters: filter ? setFilterValues({}, filter.dimension, [filter.value]) : {},
                });
                setExplorerSet(true);
                setView('explorer');
              }}
            />
          )}
          {view === 'explorer' && (
            <ExplorerTab
              sites={sites}
              refresh={refresh}
              state={explorer}
              applyDefault={!explorerSet}
              onChange={(state) => {
                setExplorer(state);
                setExplorerSet(true);
              }}
            />
          )}
          {view === 'recommendations' && <RecommendationsTab sites={sites} refresh={refresh} />}
          {view === 'budgets' && <BudgetsTab sites={sites} refresh={refresh} />}
        </>
      )}
    </div>
  );
};
