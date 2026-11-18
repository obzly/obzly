import { Button, Tooltip } from 'antd';
import React, { useMemo } from 'react';

import type { RuleRequest } from '../../../../entity/alerting';
import type { SQLQueryResponse } from '../../../../entity/query';
import {
  type SqlChartKind,
  resolveSqlChartKind,
  sqlChartShape,
} from '../../../../shared/lib/sqlResultShape';
import {
  sqlAlertDraft,
  sqlAlertUnavailableReason,
  sqlPanel,
  sqlPanelType,
  sqlPanelTypeName,
  sqlResultName,
} from '../../lib/sqlResultActions';
import { SaveToDashboardComponent } from './SaveToDashboardComponent';

interface Props {
  projectId: string;
  /** The SQL that produced response, not the editor's current text. */
  sql: string;
  response: SQLQueryResponse;
  view: 'lines' | 'chart';
  pickedKind: SqlChartKind | null;
  onCreateAlert?: (draft: Partial<RuleRequest>) => void;
}

/** Save to dashboard and Create alert for the SQL result on screen. */
export const SQLResultActionsComponent = ({
  projectId,
  sql,
  response,
  view,
  pickedKind,
  onCreateAlert,
}: Props): React.JSX.Element => {
  const kind = useMemo(
    () => resolveSqlChartKind(sqlChartShape(response.columns, response.rows), pickedKind),
    [response.columns, response.rows, pickedKind],
  );
  const panel = sqlPanel(
    sql,
    sqlPanelType(view, kind),
    sqlResultName(sql, response.columns, response.rows),
  );
  const alertUnavailable = sqlAlertUnavailableReason(response.columns, response.rows);

  return (
    <>
      <SaveToDashboardComponent
        key={JSON.stringify(panel)}
        projectId={projectId}
        panel={panel}
        typeName={sqlPanelTypeName(view, kind)}
        hint={`Saved as a ${sqlPanelTypeName(view, kind)} panel running this SQL over the dashboard's time range. Dashboard variables and filters don't apply to it.`}
      />
      {onCreateAlert && (
        <Tooltip title={alertUnavailable}>
          <Button
            disabled={alertUnavailable !== null}
            onClick={() => onCreateAlert(sqlAlertDraft(sql, response.columns, response.rows))}
          >
            Create alert
          </Button>
        </Tooltip>
      )}
    </>
  );
};
