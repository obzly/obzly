import { FunctionOutlined } from '@ant-design/icons';
import { Select } from 'antd';
import React, { useEffect, useState } from 'react';

import { type Variable } from '../../../entity/dashboards';
import { metricApi } from '../../../entity/metrics';
import { traceApi } from '../../../entity/traces';

interface Props {
  projectId: string;
  variables: Variable[];
  values: Record<string, string>;
  onChange: (values: Record<string, string>) => void;
}

// One dropdown per author-defined variable; opt-in per dashboard, alongside
// the fixed DashboardFilterBarComponent.
export const VariableBarComponent = ({
  projectId,
  variables,
  values,
  onChange,
}: Props): React.JSX.Element | null => {
  const [options, setOptions] = useState<Record<string, string[]>>({});

  useEffect(() => {
    setOptions({});

    variables.forEach((variable) => {
      if (variable.source === 'static') {
        setOptions((prev) => ({ ...prev, [variable.key]: variable.staticValues ?? [] }));
      } else if (variable.source === 'metricLabel' && variable.metricLabelName) {
        metricApi
          .listLabelValues(projectId, { labelName: variable.metricLabelName })
          .then((r) => setOptions((prev) => ({ ...prev, [variable.key]: r.values })))
          .catch(() => setOptions((prev) => ({ ...prev, [variable.key]: [] })));
      } else if (variable.source === 'services') {
        traceApi
          .listServices(projectId)
          .then((r) => setOptions((prev) => ({ ...prev, [variable.key]: r.services })))
          .catch(() => setOptions((prev) => ({ ...prev, [variable.key]: [] })));
      }
    });
  }, [projectId, variables]);

  if (variables.length === 0) return null;

  return (
    <div className="mb-2 flex flex-wrap items-center gap-1.5 rounded border border-gray-100 bg-gray-50 px-2 py-1 dark:border-gray-700 dark:bg-gray-900">
      <FunctionOutlined className="text-gray-400" />
      {variables.map((variable) => (
        <Select
          size="small"
          key={variable.key}
          allowClear
          showSearch
          placeholder={variable.label || variable.key}
          className="w-40"
          value={values[variable.key]}
          onChange={(value) => {
            const next = { ...values };
            if (value) {
              next[variable.key] = value;
            } else {
              delete next[variable.key];
            }
            onChange(next);
          }}
          options={(options[variable.key] ?? []).map((v) => ({ value: v, label: v }))}
        />
      ))}
    </div>
  );
};
