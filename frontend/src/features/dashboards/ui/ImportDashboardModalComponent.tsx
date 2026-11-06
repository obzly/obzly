import { Alert, App, Input, Modal, Select } from 'antd';
import React, { useState } from 'react';

import {
  type Dashboard,
  type DashboardImportFormat,
  dashboardApi,
} from '../../../entity/dashboards';

const { TextArea } = Input;

interface Props {
  projectId: string;
  onImported: (dashboard: Dashboard) => void;
  onCancel: () => void;
}

export const ImportDashboardModalComponent = ({
  projectId,
  onImported,
  onCancel,
}: Props): React.JSX.Element => {
  const { message } = App.useApp();
  const [format, setFormat] = useState<DashboardImportFormat>('grafana');
  const [json, setJson] = useState('');
  const [isImporting, setIsImporting] = useState(false);

  const handleImport = async () => {
    if (!json.trim()) {
      message.error('Paste the dashboard JSON export first');
      return;
    }

    setIsImporting(true);
    try {
      const response = await dashboardApi.importDashboard(projectId, { format, json });
      for (const warning of response.warnings ?? []) {
        message.warning(warning, 6);
      }
      onImported(response.dashboard);
    } catch (e) {
      message.error((e as Error).message);
    } finally {
      setIsImporting(false);
    }
  };

  return (
    <Modal
      open
      title="Import dashboard"
      onCancel={onCancel}
      onOk={handleImport}
      okText="Import"
      confirmLoading={isImporting}
      width={640}
      destroyOnHidden
    >
      <div className="space-y-4">
        <Alert
          type="info"
          showIcon
          message="Layout comes across, queries don't"
          description="Panel titles, chart types, and grid layout are imported. Query languages differ between tools, so each panel's query is left empty - edit it afterward to add one."
        />

        <div>
          <label className="mb-1 block text-sm font-medium text-gray-700 dark:text-gray-200">
            Source
          </label>
          <Select
            value={format}
            onChange={setFormat}
            className="w-56"
            options={[
              { value: 'grafana', label: 'Grafana dashboard JSON' },
              { value: 'openobserve', label: 'OpenObserve dashboard JSON' },
            ]}
          />
        </div>

        <div>
          <label className="mb-1 block text-sm font-medium text-gray-700 dark:text-gray-200">
            Dashboard JSON
          </label>
          <TextArea
            value={json}
            onChange={(e) => setJson(e.target.value)}
            rows={12}
            placeholder="Paste the exported dashboard JSON here"
            className="font-mono text-xs"
          />
        </div>
      </div>
    </Modal>
  );
};
