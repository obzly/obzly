import { FilterOutlined } from '@ant-design/icons';
import { Select } from 'antd';
import React, { useEffect, useState } from 'react';

import { metricApi } from '../../../entity/metrics';
import { traceApi } from '../../../entity/traces';
import { type DashboardFilters, hasActiveFilters } from '../lib/dashboardFilters';

interface Props {
  projectId: string;
  filters: DashboardFilters;
  onChange: (filters: DashboardFilters) => void;
}

// Namespace/Pod/Node/Service filters applied to each panel's query at run
// time (dashboardFilters.ts), not saved onto the dashboard. Options load once
// per project and don't cascade.
export const DashboardFilterBarComponent = ({
  projectId,
  filters,
  onChange,
}: Props): React.JSX.Element => {
  const [namespaces, setNamespaces] = useState<string[]>([]);
  const [pods, setPods] = useState<string[]>([]);
  const [nodes, setNodes] = useState<string[]>([]);
  const [services, setServices] = useState<string[]>([]);

  useEffect(() => {
    setNamespaces([]);
    setPods([]);
    setNodes([]);
    setServices([]);

    metricApi
      .listLabelValues(projectId, { labelName: 'k8s_namespace_name' })
      .then((r) => setNamespaces(r.values))
      .catch(() => setNamespaces([]));
    metricApi
      .listLabelValues(projectId, { labelName: 'k8s_pod_name' })
      .then((r) => setPods(r.values))
      .catch(() => setPods([]));
    metricApi
      .listLabelValues(projectId, { labelName: 'k8s_node_name' })
      .then((r) => setNodes(r.values))
      .catch(() => setNodes([]));
    traceApi
      .listServices(projectId)
      .then((r) => setServices(r.services))
      .catch(() => setServices([]));
  }, [projectId]);

  // Nothing to filter by at all - don't show an empty, useless bar.
  if (namespaces.length === 0 && pods.length === 0 && nodes.length === 0 && services.length === 0) {
    return <></>;
  }

  const set = (key: keyof DashboardFilters) => (value: string | undefined) =>
    onChange({ ...filters, [key]: value });

  return (
    <div className="mb-2 flex flex-wrap items-center gap-1.5 rounded border border-gray-100 bg-gray-50 px-2 py-1 dark:border-gray-700 dark:bg-gray-900">
      <FilterOutlined className="text-gray-400" />
      {namespaces.length > 0 && (
        <Select
          size="small"
          allowClear
          showSearch
          placeholder="Namespace"
          className="w-40"
          value={filters.namespace}
          onChange={set('namespace')}
          options={namespaces.map((v) => ({ value: v, label: v }))}
        />
      )}
      {pods.length > 0 && (
        <Select
          size="small"
          allowClear
          showSearch
          placeholder="Pod"
          className="w-48"
          value={filters.pod}
          onChange={set('pod')}
          options={pods.map((v) => ({ value: v, label: v }))}
        />
      )}
      {nodes.length > 0 && (
        <Select
          size="small"
          allowClear
          showSearch
          placeholder="Node"
          className="w-56"
          value={filters.node}
          onChange={set('node')}
          options={nodes.map((v) => ({ value: v, label: v }))}
        />
      )}
      {services.length > 0 && (
        <Select
          size="small"
          allowClear
          showSearch
          placeholder="Service"
          className="w-40"
          value={filters.service}
          onChange={set('service')}
          options={services.map((v) => ({ value: v, label: v }))}
        />
      )}
      {hasActiveFilters(filters) && (
        <button
          type="button"
          className="text-sm text-blue-600 hover:underline dark:text-blue-400"
          onClick={() => onChange({})}
        >
          Clear filters
        </button>
      )}
    </div>
  );
};
