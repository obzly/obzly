import React from 'react';

import type { QueryNode } from '../../../entity/query';
import { WebAnalyticsComponent } from '../../webAnalytics';
import { LogHealthMapTabComponent } from './LogHealthMapTabComponent';
import { LogPatternsTabComponent } from './LogPatternsTabComponent';
import { LogVolumeTabComponent } from './LogVolumeTabComponent';
import type { TimeRange } from './TimeRangePickerComponent';

interface Props {
  /** A LOGS_VIEWS key other than Search. */
  view: string;
  projectId: string;
  /** The search on screen (null: every log). */
  query: QueryNode | null;
  searchText: string;
  /** Back to Search showing (exclude: hiding) a pattern over range. */
  onPatternFilter: (hash: string, exclude: boolean, range: TimeRange) => void;
  /** Back to Search on these clauses over range. */
  onOpenLogs: (clauses: string[], range: TimeRange) => void;
  /** Back to Search over range with "field = value" (exclude: "≠") added. */
  onFilter: (field: string, value: string, exclude: boolean, range: TimeRange) => void;
  onOpenInfrastructure?: () => void;
}

/** The Logs page's views besides Search (see LOGS_VIEWS). */
export const LogsInsightViewComponent = ({
  view,
  projectId,
  query,
  searchText,
  onPatternFilter,
  onOpenLogs,
  onFilter,
  onOpenInfrastructure,
}: Props): React.JSX.Element | null => {
  switch (view) {
    case 'patterns':
      return (
        <LogPatternsTabComponent
          projectId={projectId}
          query={query}
          searchText={searchText}
          onPatternFilter={onPatternFilter}
        />
      );
    case 'volume':
      return <LogVolumeTabComponent projectId={projectId} />;
    case 'health':
      return (
        <LogHealthMapTabComponent
          projectId={projectId}
          query={query}
          searchText={searchText}
          onOpenLogs={onOpenLogs}
          onFilter={onFilter}
          onOpenInfrastructure={onOpenInfrastructure}
        />
      );
    case 'web':
      return <WebAnalyticsComponent projectId={projectId} onOpenLogs={onOpenLogs} />;
    default:
      return null;
  }
};
