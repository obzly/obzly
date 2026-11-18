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
