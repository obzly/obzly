// Posture lives on each Site; a control plane reads every Site it can reach and adds them up.
import { LoadingOutlined, ReloadOutlined } from '@ant-design/icons';
import { Alert, Button, Spin, Tabs } from 'antd';
import { useEffect, useState } from 'react';

import type { PostureFilters } from '../../../entity/posture';
import { useVisibleInterval } from '../../../shared/lib';
import { type CostSite, listSites } from '../../cost';
import { DEFAULT_TRENDS, type TrendsState } from '../lib/format';
import { ActionsTab } from './ActionsTab';
import { CoverageTab } from './CoverageTab';
import { FindingsTab } from './FindingsTab';
import { OverviewTab } from './OverviewTab';
import { PostureAlertsButton } from './PostureAlertsButton';
import { TrendsTab } from './TrendsTab';

interface Props {
  contentHeight: number;
  onManageCloud?: () => void;
}

type View = 'overview' | 'findings' | 'actions' | 'trends' | 'coverage';

// Posture refreshes every few hours; this only picks up a refresh while the tab stays open.
const REFRESH_MS = 15 * 60_000;

export const PostureComponent = ({ contentHeight, onManageCloud }: Props) => {
  const [view, setView] = useState<View>('overview');
  const [sites, setSites] = useState<CostSite[]>();
  const [error, setError] = useState<string>();
  const [refresh, setRefresh] = useState(0);
  const [filters, setFilters] = useState<PostureFilters>({});
  const [trends, setTrends] = useState<TrendsState>(DEFAULT_TRENDS);

  useEffect(() => {
    listSites()
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
        <h1 className="m-0 text-xl font-semibold text-gray-900 dark:text-gray-100">Posture</h1>
        <span className="text-sm text-gray-500 dark:text-gray-400">
          Findings against security standards, from your cloud connections
        </span>
        <div className="flex-1" />
        {sites && <PostureAlertsButton sites={sites} />}
        <Button icon={<ReloadOutlined />} onClick={() => setRefresh((value) => value + 1)}>
          Refresh
        </Button>
      </div>
      {error && <Alert className="my-3" type="error" showIcon title={error} />}
      <Tabs
        activeKey={view}
        onChange={(key) => setView(key as View)}
        items={[
          { key: 'overview', label: 'Standards' },
          { key: 'findings', label: 'Findings' },
          { key: 'actions', label: 'Recommendations' },
          { key: 'trends', label: 'Trends' },
          { key: 'coverage', label: 'Coverage' },
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
              onCoverage={() => setView('coverage')}
              onFindings={({ severity, standard }) => {
                setFilters({
                  severity: severity ? [severity] : undefined,
                  standard: standard ? [standard] : undefined,
                });
                setView('findings');
              }}
            />
          )}
          {view === 'findings' && (
            <FindingsTab sites={sites} refresh={refresh} filters={filters} onChange={setFilters} />
          )}
          {view === 'actions' && <ActionsTab sites={sites} refresh={refresh} />}
          {view === 'trends' && (
            <TrendsTab sites={sites} refresh={refresh} state={trends} onChange={setTrends} />
          )}
          {view === 'coverage' && <CoverageTab sites={sites} refresh={refresh} />}
        </>
      )}
    </div>
  );
};
