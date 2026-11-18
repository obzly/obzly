// Cost lives on each Site; a control plane reads every Site it can reach and adds the answers up
// per currency.
import { ReloadOutlined } from '@ant-design/icons';
import { Alert, Button } from 'antd';
import { useEffect, useState } from 'react';

import { useVisibleInterval } from '../../../shared/lib';
import { PageHeader } from '../../../shared/ui/v2';
import type { PostureView } from '../../posture/ui/PostureComponent';
import { DEFAULT_EXPLORER, type ExplorerState, setFilterValues } from '../lib/explorer';
import type { CostSite } from '../lib/merge';
import { fitGranularity, resolveRange } from '../lib/ranges';
import { listCostSites } from '../lib/sites';
import { BudgetsTab } from './BudgetsTab';
import { ExplorerTab } from './ExplorerTab';
import { OverviewTab } from './OverviewTab';
import { RecommendationsTab } from './RecommendationsTab';
import { SummaryTab } from './SummaryTab';
import { Loading, ViewTabs } from './parts';

interface Props {
  contentHeight: number;
  /** Admins turn Cost on for a cloud connection in settings. */
  onManageCloud?: () => void;
  /** Opens the Posture page on a view, from the Summary's posture panels. */
  onOpenPosture?: (view: PostureView) => void;
}

type View = 'summary' | 'overview' | 'explorer' | 'recommendations' | 'budgets';

// Cost changes a few times a day; this only picks up a delivery while the tab stays open.
const REFRESH_MS = 15 * 60_000;

export const CostComponent = ({ contentHeight, onManageCloud, onOpenPosture }: Props) => {
  const [view, setView] = useState<View>('summary');
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
      className="flex w-full flex-col gap-4 overflow-y-auto rounded-2xl border border-gray-200 bg-white p-5 dark:border-gray-700 dark:bg-gray-800"
      style={{ height: contentHeight }}
    >
      <PageHeader
        title="Cost"
        subtitle={
          (sites?.length ?? 0) > 1 ? 'All Sites you can reach' : 'Cloud spend from your connections'
        }
        actions={
          <Button icon={<ReloadOutlined />} onClick={() => setRefresh((value) => value + 1)}>
            Refresh
          </Button>
        }
      />

      {error && <Alert type="error" showIcon title={error} />}

      <ViewTabs<View>
        label="Cost views"
        value={view}
        onChange={setView}
        items={[
          { key: 'summary', label: 'Summary' },
          { key: 'overview', label: 'Overview' },
          { key: 'explorer', label: 'Explorer' },
          { key: 'recommendations', label: 'Recommendations' },
          { key: 'budgets', label: 'Budgets & anomalies' },
        ]}
      />

      {!sites ? (
        !error && <Loading />
      ) : (
        <>
          {view === 'summary' && (
            <SummaryTab
              sites={sites}
              refresh={refresh}
              onManageCloud={onManageCloud}
              onOpenPosture={onOpenPosture}
              onView={setView}
              onExploreServices={() => {
                const range = resolveRange('mtd', new Date().toISOString().slice(0, 10));
                setExplorer({
                  ...DEFAULT_EXPLORER,
                  range: 'mtd',
                  granularity: fitGranularity(range),
                  groupBy: 'service',
                  filters: {},
                });
                setExplorerSet(true);
                setView('explorer');
              }}
            />
          )}
          {view === 'overview' && (
            <OverviewTab
              sites={sites}
              refresh={refresh}
              onManageCloud={onManageCloud}
              onAnomalies={() => setView('budgets')}
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
