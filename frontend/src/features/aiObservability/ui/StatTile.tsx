import { InfoCircleOutlined } from '@ant-design/icons';
import { Tooltip } from 'antd';
import type React from 'react';

interface Props {
  label: string;
  value: string;
  hint?: string;
  tooltip?: string;
}

export const StatTile = ({ label, value, hint, tooltip }: Props): React.JSX.Element => (
  <div className="rounded-lg border border-gray-200 bg-white p-4 dark:border-gray-700 dark:bg-gray-800">
    <div className="flex items-center gap-1 text-xs text-gray-500 dark:text-gray-400">
      {label}
      {tooltip && (
        <Tooltip title={tooltip}>
          <InfoCircleOutlined />
        </Tooltip>
      )}
    </div>
    <div className="mt-1 text-2xl font-semibold text-gray-900 tabular-nums dark:text-gray-100">
      {value}
    </div>
    {hint && <div className="mt-1 text-xs text-gray-500 dark:text-gray-400">{hint}</div>}
  </div>
);
