import { DeleteOutlined, LoadingOutlined } from '@ant-design/icons';
import { App, Button, Input, Popconfirm, Select, Spin } from 'antd';
import { useEffect, useState } from 'react';

import { projectApi } from '../../../entity/projects/api/projectApi';
import type {
  PromotedField,
  PromotedFieldType,
} from '../../../entity/projects/model/PromotedField';

interface Props {
  projectId: string;
  canEdit: boolean;
}

const SLOTS: Record<PromotedFieldType, number> = { string: 8, number: 4 };

const typeOf = (field: PromotedField): PromotedFieldType =>
  field.slot.startsWith('n') ? 'number' : 'string';

export const PromotedFieldsComponent = ({ projectId, canEdit }: Props) => {
  const { message } = App.useApp();
  const [fields, setFields] = useState<PromotedField[]>([]);
  const [isLoading, setIsLoading] = useState(true);
  const [newField, setNewField] = useState('');
  const [newType, setNewType] = useState<PromotedFieldType>('string');
  const [isSaving, setIsSaving] = useState(false);

  const load = async () => {
    setIsLoading(true);
    try {
      setFields(await projectApi.getPromotedFields(projectId));
    } catch (error: unknown) {
      message.error(error instanceof Error ? error.message : 'Failed to load promoted fields');
    } finally {
      setIsLoading(false);
    }
  };

  const promote = async () => {
    setIsSaving(true);
    try {
      const promoted = await projectApi.promoteField(projectId, newField.trim(), newType);
      setFields((prev) => [...prev, promoted]);
      setNewField('');
      message.success(`Promoted ${promoted.field}`);
    } catch (error: unknown) {
      message.error(error instanceof Error ? error.message : 'Failed to promote field');
    } finally {
      setIsSaving(false);
    }
  };

  const retire = async (field: string) => {
    try {
      await projectApi.retirePromotedField(projectId, field);
      setFields((prev) => prev.filter((f) => f.field !== field));
      message.success(`${field} is no longer promoted`);
    } catch (error: unknown) {
      message.error(error instanceof Error ? error.message : 'Failed to remove promotion');
    }
  };

  useEffect(() => {
    load();
  }, [projectId]);

  if (isLoading) {
    return <Spin indicator={<LoadingOutlined spin />} />;
  }

  return (
    <div>
      <div className="mb-3 text-sm text-gray-500 dark:text-gray-300">
        Log fields you filter or group by often, copied into their own columns at ingest so searches
        skip parsing every log&apos;s fields. Number fields also support &gt; and &lt; filters.
        Applies to logs received from now on; older logs are read as before. Up to {SLOTS.string}{' '}
        text and {SLOTS.number} number fields, and a removed field&apos;s column isn&apos;t reused.
      </div>
      {fields.length === 0 && (
        <div className="mb-3 text-sm text-gray-400 dark:text-gray-400">No promoted fields</div>
      )}
      {fields.map((field) => (
        <div key={field.slot} className="mb-2 flex items-center gap-3">
          <span className="w-56 truncate font-mono text-sm text-gray-900 dark:text-gray-100">
            {field.field}
          </span>
          <span className="w-16 text-xs text-gray-500 dark:text-gray-300">{typeOf(field)}</span>
          {canEdit && (
            <Popconfirm
              title={`Stop promoting ${field.field}?`}
              okText="Remove"
              okButtonProps={{ danger: true }}
              onConfirm={() => retire(field.field)}
            >
              <Button size="small" icon={<DeleteOutlined />} />
            </Popconfirm>
          )}
        </div>
      ))}
      {canEdit && (
        <div className="mt-3 flex items-center gap-2">
          <Input
            placeholder="Field name, e.g. http.status_code"
            value={newField}
            onChange={(e) => setNewField(e.target.value)}
            onPressEnter={() => newField.trim() && promote()}
            style={{ width: '260px' }}
          />
          <Select
            value={newType}
            onChange={setNewType}
            style={{ width: '110px' }}
            options={[
              { value: 'string', label: 'Text' },
              { value: 'number', label: 'Number' },
            ]}
          />
          <Button
            type="primary"
            onClick={promote}
            loading={isSaving}
            disabled={!newField.trim() || isSaving}
            className="border-emerald-600 bg-emerald-600 hover:border-emerald-700 hover:bg-emerald-700"
          >
            Promote
          </Button>
        </div>
      )}
    </div>
  );
};
