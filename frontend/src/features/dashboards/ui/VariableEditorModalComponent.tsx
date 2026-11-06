import { DeleteOutlined, PlusOutlined } from '@ant-design/icons';
import { Button, Input, Modal, Select } from 'antd';
import React, { useState } from 'react';

import type { Variable, VariableSource } from '../../../entity/dashboards';

interface Props {
  variables: Variable[];
  onSave: (variables: Variable[]) => void;
  onCancel: () => void;
}

const SOURCE_OPTIONS: { value: VariableSource; label: string }[] = [
  { value: 'static', label: 'Static list' },
  { value: 'metricLabel', label: 'Metric label values' },
  { value: 'services', label: 'Trace services' },
];

const emptyVariable = (): Variable => ({
  key: '',
  label: '',
  source: 'static',
  staticValues: [],
});

// Edits a dashboard's ${key} template variables (substituted by dashboardVariables.ts).
export const VariableEditorModalComponent = ({
  variables,
  onSave,
  onCancel,
}: Props): React.JSX.Element => {
  const [draft, setDraft] = useState<Variable[]>(variables.length > 0 ? variables : []);

  const update = (index: number, patch: Partial<Variable>) => {
    setDraft((prev) => prev.map((v, i) => (i === index ? { ...v, ...patch } : v)));
  };

  const remove = (index: number) => {
    setDraft((prev) => prev.filter((_, i) => i !== index));
  };

  const isValid = draft.every(
    (v) =>
      /^[A-Za-z_][A-Za-z0-9_]*$/.test(v.key) &&
      (v.source !== 'metricLabel' || (v.metricLabelName ?? '').trim() !== '') &&
      (v.source !== 'static' || (v.staticValues ?? []).length > 0),
  );
  const hasDuplicateKeys = new Set(draft.map((v) => v.key)).size !== draft.length;

  return (
    <Modal
      title="Dashboard variables"
      open
      onCancel={onCancel}
      onOk={() => onSave(draft)}
      okButtonProps={{ disabled: !isValid || hasDuplicateKeys }}
      width={640}
    >
      <p className="mb-3 text-sm text-gray-500 dark:text-gray-400">
        Reference a variable in any panel&apos;s query as{' '}
        <code className="rounded bg-gray-100 px-1 dark:bg-gray-800">${'{key}'}</code>. Its selected
        value (from the dropdown bar above the panel grid) gets substituted in before the query
        runs.
      </p>

      {hasDuplicateKeys && (
        <p className="mb-2 text-sm text-red-500">Variable keys must be unique.</p>
      )}

      <div className="space-y-3">
        {draft.map((variable, index) => (
          <div key={index} className="rounded border border-gray-200 p-3 dark:border-gray-700">
            <div className="mb-2 flex items-center gap-2">
              <Input
                placeholder="key (e.g. cluster)"
                className="w-40"
                value={variable.key}
                onChange={(e) => update(index, { key: e.target.value.trim() })}
              />
              <Input
                placeholder="Label (e.g. Cluster)"
                className="w-40"
                value={variable.label}
                onChange={(e) => update(index, { label: e.target.value })}
              />
              <Select
                className="w-48"
                value={variable.source}
                options={SOURCE_OPTIONS}
                onChange={(source: VariableSource) => update(index, { source })}
              />
              <Button danger type="text" icon={<DeleteOutlined />} onClick={() => remove(index)} />
            </div>

            {variable.source === 'metricLabel' && (
              <Input
                placeholder="Metric label name (e.g. k8s.namespace.name)"
                value={variable.metricLabelName ?? ''}
                onChange={(e) => update(index, { metricLabelName: e.target.value })}
              />
            )}
            {variable.source === 'static' && (
              <Select
                mode="tags"
                className="w-full"
                placeholder="Type values and press enter"
                value={variable.staticValues ?? []}
                onChange={(staticValues) => update(index, { staticValues })}
              />
            )}
          </div>
        ))}
      </div>

      <Button
        className="mt-3"
        icon={<PlusOutlined />}
        onClick={() => setDraft((prev) => [...prev, emptyVariable()])}
      >
        Add variable
      </Button>
    </Modal>
  );
};
