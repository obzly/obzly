// Posture lives on each Site; a control plane reads every Site it can reach and adds them up.
import { ReloadOutlined } from '@ant-design/icons';
import { Alert, Button } from 'antd';
import { useEffect, useState } from 'react';

import type { PostureFilters } from '../../../entity/posture';
import { useVisibleInterval } from '../../../shared/lib';
import { PageHeader } from '../../../shared/ui/v2';
import { type CostSite, Loading, ViewTabs, listSites } from '../../cost';
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
  /** The view to open on (the Cost summary links straight to Findings or Coverage). */
  initialView?: PostureView;
}

export type PostureView = 'overview' | 'findings' | 'actions' | 'trends' | 'coverage';
type View = PostureView;

// Posture refreshes every few hours; this only picks up a refresh while the tab stays open.
const REFRESH_MS = 15 * 60_000;

export const PostureComponent = ({ contentHeight, onManageCloud, initialView }: Props) => {
  const [view, setView] = useState<View>(initialView ?? 'overview');
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
      className="flex w-full flex-col gap-4 overflow-y-auto rounded-2xl border border-gray-200 bg-white p-5 dark:border-gray-700 dark:bg-gray-800"
      style={{ height: contentHeight }}
    >
      <PageHeader
        title="Posture"
        subtitle="Findings against security standards, from your cloud connections"
        actions={
          <>
            {sites && <PostureAlertsButton sites={sites} />}
            <Button icon={<ReloadOutlined />} onClick={() => setRefresh((value) => value + 1)}>
              Refresh
            </Button>
          </>
        }
      />
      {error && <Alert type="error" showIcon title={error} />}
      <ViewTabs<View>
        label="Posture views"
        value={view}
        onChange={setView}
        items={[
          { key: 'overview', label: 'Standards' },
          { key: 'findings', label: 'Findings' },
          { key: 'actions', label: 'Recommendations' },
          { key: 'trends', label: 'Trends' },
          { key: 'coverage', label: 'Coverage' },
        ]}
      />
      {!sites ? (
        !error && <Loading />
      ) : (
        <>
          {view === 'overview' && (
            <OverviewTab
              sites={sites}
              refresh={refresh}
              onManageCloud={onManageCloud}
              onCoverage={() => setView('coverage')}
              onFindings={({ severity, standard, status }) => {
                setFilters({
                  status,
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
