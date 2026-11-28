/**
 * The Logs page's views (the tab bar in its header). Logs features from other
 * workstreams add their tab to LOGS_VIEWS and their view to the switch in
 * LogsInsightViewComponent.
 */
export interface LogsViewTab {
  key: string;
  label: string;
  /** Shows the "v2" badge. */
  isNew?: boolean;
}

export const LOGS_SEARCH_VIEW = 'search';

export const LOGS_VIEWS: LogsViewTab[] = [
  { key: LOGS_SEARCH_VIEW, label: 'Search' },
  { key: 'patterns', label: 'Patterns', isNew: true },
  { key: 'volume', label: 'Volume & noise', isNew: true },
  { key: 'health', label: 'Health map', isNew: true },
  { key: 'web', label: 'Web analytics', isNew: true },
];

const LOGS_VIEW_STORAGE_KEY = 'obzly-logs-view';

// Per browser tab (sessionStorage), like the selected page: a reload keeps the
// view, a new visit opens Search.
export const readStoredLogsView = (): string => {
  try {
    const stored = sessionStorage.getItem(LOGS_VIEW_STORAGE_KEY);
    return LOGS_VIEWS.some((view) => view.key === stored) ? (stored as string) : LOGS_SEARCH_VIEW;
  } catch {
    return LOGS_SEARCH_VIEW;
  }
};

export const storeLogsView = (view: string): void => {
  try {
    sessionStorage.setItem(LOGS_VIEW_STORAGE_KEY, view);
  } catch {
    /* a reload opens Search */
  }
};
