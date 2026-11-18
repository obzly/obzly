import { DeleteOutlined, LoadingOutlined } from '@ant-design/icons';
import { App, Button, Input, Popconfirm, Select, Spin, Tooltip } from 'antd';
import { useEffect, useState } from 'react';

import { projectApi } from '../../../entity/projects/api/projectApi';
import type {
  PromotedField,
  PromotedFieldType,
} from '../../../entity/projects/model/PromotedField';
import { Pill } from './ProjectPageShell';

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
    <div className="flex flex-col gap-3">
      <div className="text-xs text-gray-500 dark:text-gray-400">
        Log fields you filter or group by often, copied into their own columns at ingest so searches
        skip parsing every log&apos;s fields. Number fields also support &gt; and &lt; filters.
        Applies to logs received from now on; older logs are read as before. Up to {SLOTS.string}{' '}
        text and {SLOTS.number} number fields, and a removed field&apos;s column isn&apos;t reused.
      </div>
      {fields.length === 0 && (
        <div className="text-sm text-gray-400 dark:text-gray-400">No promoted fields</div>
      )}
      {fields.length > 0 && (
        <div className="flex flex-col">
          {fields.map((field) => (
            <div
              key={field.slot}
              className="flex items-center gap-2.5 border-b border-gray-200 py-1.5 last:border-b-0 dark:border-gray-700"
            >
              <span className="min-w-0 flex-1 truncate font-mono text-sm text-gray-900 dark:text-gray-100">
                {field.field}
              </span>
              <Pill tone="neutral">{typeOf(field)}</Pill>
              {canEdit && (
                <Tooltip title="Stop promoting">
                  <Popconfirm
                    title={`Stop promoting ${field.field}?`}
                    okText="Remove"
                    okButtonProps={{ danger: true }}
                    onConfirm={() => retire(field.field)}
                  >
                    <Button
                      size="small"
                      type="text"
                      aria-label={`Stop promoting ${field.field}`}
                      icon={<DeleteOutlined />}
                    />
                  </Popconfirm>
                </Tooltip>
              )}
            </div>
          ))}
        </div>
      )}
      {canEdit && (
        <div className="flex flex-wrap items-center gap-2">
          <Input
            placeholder="Field name, e.g. http.status_code"
            aria-label="Field name to promote"
            value={newField}
            onChange={(e) => setNewField(e.target.value)}
            onPressEnter={() => newField.trim() && promote()}
            className="!font-mono"
            style={{ flex: '1 1 200px', minWidth: 0 }}
          />
          <Select
            aria-label="Field type"
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
          >
            Promote
          </Button>
        </div>
      )}
    </div>
  );
};
