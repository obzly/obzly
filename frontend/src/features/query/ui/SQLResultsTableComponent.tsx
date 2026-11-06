import { Alert, Empty, Table } from 'antd';
import React from 'react';

import type { SQLQueryResponse } from '../../../entity/query';

interface Props {
  response: SQLQueryResponse | null;
  isExecuting: boolean;
}

/** A SQL-mode result; columns come from the response since a SELECT can project any shape. */
export const SQLResultsTableComponent = ({
  response,
  isExecuting,
}: Props): React.JSX.Element | null => {
  if (!response) return null;

  const columns = response.columns.map((name, index) => ({
    title: name,
    dataIndex: String(index),
    key: name,
    ellipsis: true,
    render: (value: unknown) => formatCell(value),
  }));

  const dataSource = response.rows.map((row, rowIndex) => {
    const record: Record<string, unknown> = { key: rowIndex };
    row.forEach((value, colIndex) => {
      record[String(colIndex)] = value;
    });
    return record;
  });

  return (
    <div className="w-full space-y-2">
      <div className="flex items-center justify-between text-sm text-gray-500 dark:text-gray-300">
        <span>
          {response.rowCount.toLocaleString()} row{response.rowCount === 1 ? '' : 's'}
          {response.truncated && ' (truncated - narrow your query or add a LIMIT)'} ·{' '}
          {Math.round(parseFloat(response.executedInMs)).toLocaleString()} ms
        </span>
      </div>

      {response.truncated && (
        <Alert
          type="warning"
          showIcon
          message="Result truncated"
          description="This query matched more rows than the server returns in one response - add a LIMIT clause or narrow your WHERE/time range for a complete result."
        />
      )}

      {dataSource.length === 0 ? (
        <Empty description="No rows" />
      ) : (
        <Table
          size="small"
          loading={isExecuting}
          columns={columns}
          dataSource={dataSource}
          pagination={{ pageSize: 50, showSizeChanger: false }}
          scroll={{ x: 'max-content' }}
        />
      )}
    </div>
  );
};

function formatCell(value: unknown): string {
  if (value === null || value === undefined) return '';
  if (typeof value === 'object') return JSON.stringify(value);
  return String(value);
}
