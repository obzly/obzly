import { ToolOutlined } from '@ant-design/icons';
import { Tooltip } from 'antd';

import { maintenanceTitle } from '../lib/maintenance';
import { STATUS_COLOR } from '../lib/status';
import type { MonitorMaintenance } from '../lib/useMonitorData';

/** A wrench next to a monitor in maintenance or entering it within the hour. */
export const MaintenanceMarker = ({ maintenance }: { maintenance?: MonitorMaintenance }) => {
  const title = maintenanceTitle(maintenance);
  if (!title) return null;
  return (
    <Tooltip title={title}>
      <ToolOutlined
        aria-label={title}
        className="text-xs"
        style={{ color: STATUS_COLOR.maintenance, opacity: maintenance?.active ? 1 : 0.6 }}
      />
    </Tooltip>
  );
};
