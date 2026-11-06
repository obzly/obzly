export { dashboardApi } from './api/dashboardApi';

export type { AggregationBucket, AggregationResponse } from './model/AggregationResponse';
export type { Dashboard } from './model/Dashboard';
export type {
  CreateDashboardRequest,
  DashboardImportFormat,
  ImportDashboardRequest,
  ImportDashboardResponse,
  ListDashboardsResponse,
  ListFoldersResponse,
  PanelQueryRequest,
  UpdateDashboardRequest,
} from './model/DashboardRequests';
export type { Folder } from './model/Folder';
export { AUTO_REFRESH_OPTIONS, type AutoRefreshIntervalMs } from './model/AutoRefreshInterval';
export type { GridPos } from './model/GridPos';
export type { Panel } from './model/Panel';
export { effectivePanelSource } from './model/PanelSource';
export type { PanelSource, SpanGroupField, SpanMetric } from './model/PanelSource';
export type { PanelType } from './model/PanelType';
export type { Tab } from './model/Tab';
export type { TimeRangeDTO } from './model/TimeRangeDTO';
export type { Variable, VariableSource } from './model/Variable';
