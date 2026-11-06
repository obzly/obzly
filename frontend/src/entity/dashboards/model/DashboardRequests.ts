import type { Dashboard } from './Dashboard';
import type { Folder } from './Folder';
import type { Panel } from './Panel';
import type { Tab } from './Tab';
import type { TimeRangeDTO } from './TimeRangeDTO';
import type { Variable } from './Variable';

export interface CreateDashboardRequest {
  title: string;
  description?: string;
  tabs?: Tab[];
  variables?: Variable[];
}

export interface UpdateDashboardRequest {
  title: string;
  description?: string;
  tabs: Tab[];
  variables?: Variable[];
}

export interface ListDashboardsResponse {
  dashboards: Dashboard[];
}

export interface ListFoldersResponse {
  folders: Folder[];
}

// Always the full panel definition, so unsaved edits can be previewed.
export interface PanelQueryRequest {
  panel: Panel;
  timeRange?: TimeRangeDTO;
}

export type DashboardImportFormat = 'grafana' | 'openobserve';

export interface ImportDashboardRequest {
  format: DashboardImportFormat;
  json: string;
}

export interface ImportDashboardResponse {
  dashboard: Dashboard;
  warnings?: string[];
}
