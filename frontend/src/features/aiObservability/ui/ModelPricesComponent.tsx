import { CheckOutlined, DeleteOutlined, LoadingOutlined, PlusOutlined } from '@ant-design/icons';
import { Alert, App, Button, Input, Popconfirm, Spin, Table, Tooltip } from 'antd';
import type { ColumnsType } from 'antd/es/table';
import React, { useEffect, useState } from 'react';

import { type AiModelPricesResponse, aiBudgetsApi } from '../../../entity/aiUsage';
import { formatCount } from '../lib/format';
import { parsePrice } from '../lib/llmApps';

interface Props {
  projectId: string;
  canManage: boolean;
}

interface PriceRow {
  key: string;
  /** Unset for a model seen without a price, or one being added. */
  id?: string;
  model: string;
  input: string;
  output: string;
  cached: string;
  /** -1 unknown, null for a row being added. */
  seenCalls: number | null;
  isDirty: boolean;
}

const toText = (value: number | null) => (value === null ? '' : String(value));

const toRows = (response: AiModelPricesResponse): PriceRow[] => [
  ...response.prices.map((price) => ({
    key: price.id,
    id: price.id,
    model: price.model,
    input: toText(price.inputPerMillion),
    output: toText(price.outputPerMillion),
    cached: toText(price.cachedInputPerMillion),
    seenCalls: price.seenCalls,
    isDirty: false,
  })),
  ...response.unpricedModels.map((seen) => ({
    key: `seen:${seen.model}`,
    model: seen.model,
    input: '',
    output: '',
    cached: '',
    seenCalls: seen.calls,
    isDirty: false,
  })),
];

const seenText = (calls: number | null) => {
  if (calls === null) return '';
  if (calls < 0) return 'unknown';
  return calls > 0 ? `yes · ${formatCount(calls)} calls` : 'no';
};

export const ModelPricesComponent = ({ projectId, canManage }: Props): React.JSX.Element => {
  const { message } = App.useApp();

  const [rows, setRows] = useState<PriceRow[]>([]);
  const [seenError, setSeenError] = useState<string>();
  const [isLoading, setIsLoading] = useState(true);
  const [filledAsOf, setFilledAsOf] = useState<string | null>(null);
  const [savingKey, setSavingKey] = useState<string | null>(null);

  const load = async () => {
    try {
      const response = await aiBudgetsApi.listPrices(projectId);
      setRows(toRows(response));
      setSeenError(response.seenError);
    } catch (error: unknown) {
      message.error(error instanceof Error ? error.message : 'Failed to load model prices');
    } finally {
      setIsLoading(false);
    }
  };

  const edit = (key: string, patch: Partial<PriceRow>) =>
    setRows((prev) =>
      prev.map((row) => (row.key === key ? { ...row, ...patch, isDirty: true } : row)),
    );

  const save = async (row: PriceRow) => {
    const input = parsePrice(row.input);
    const output = parsePrice(row.output);
    const cached = parsePrice(row.cached);
    if (input === null || output === null || input === 'invalid' || output === 'invalid') {
      message.error('Set the input and output price (USD per million tokens)');
      return;
    }
    if (cached === 'invalid') {
      message.error('The cached input price must be a number, or empty to use the input price');
      return;
    }
    const request = {
      model: row.model.trim(),
      inputPerMillion: input,
      outputPerMillion: output,
      cachedInputPerMillion: cached,
    };
    setSavingKey(row.key);
    try {
      if (row.id) {
        await aiBudgetsApi.updatePrice(projectId, row.id, request);
      } else {
        await aiBudgetsApi.createPrice(projectId, request);
      }
      message.success(`Price for ${request.model} saved`);
      await load();
    } catch (error: unknown) {
      message.error(error instanceof Error ? error.message : 'Failed to save the price');
    } finally {
      setSavingKey(null);
    }
  };

  const remove = async (row: PriceRow) => {
    if (!row.id) {
      setRows((prev) => prev.filter((r) => r.key !== row.key));
      return;
    }
    try {
      await aiBudgetsApi.deletePrice(projectId, row.id);
      message.success(`Price for ${row.model} deleted`);
      await load();
    } catch (error: unknown) {
      message.error(error instanceof Error ? error.message : 'Failed to delete the price');
    }
  };

  const fillCommon = async () => {
    try {
      const response = await aiBudgetsApi.fillCommonPrices(projectId);
      setFilledAsOf(response.asOf);
      message.success(
        response.added
          ? `Added ${response.added} models`
          : 'Every common model already has a price',
      );
      await load();
    } catch (error: unknown) {
      message.error(error instanceof Error ? error.message : 'Failed to add common models');
    }
  };

  const addRow = () =>
    setRows((prev) => [
      ...prev,
      {
        key: `new:${crypto.randomUUID()}`,
        model: '',
        input: '',
        output: '',
        cached: '',
        seenCalls: null,
        isDirty: true,
      },
    ]);

  useEffect(() => {
    setIsLoading(true);
    void load();
  }, [projectId]);

  const priceInput = (row: PriceRow, field: 'input' | 'output' | 'cached', label: string) => (
    <Input
      size="small"
      className="!w-28"
      aria-label={label}
      value={row[field]}
      placeholder={field === 'cached' ? 'as input' : 'not set'}
      disabled={!canManage}
      onChange={(e) => edit(row.key, { [field]: e.target.value })}
    />
  );

  const columns: ColumnsType<PriceRow> = [
    {
      title: 'Model (exact or prefix*)',
      key: 'model',
      render: (_, row) =>
        row.key.startsWith('new:') ? (
          <Input
            size="small"
            aria-label="Model"
            placeholder="claude-sonnet-4-5*"
            value={row.model}
            onChange={(e) => edit(row.key, { model: e.target.value })}
          />
        ) : (
          <span className="font-mono text-xs">{row.model}</span>
        ),
    },
    { title: 'Input', key: 'input', render: (_, row) => priceInput(row, 'input', 'Input price') },
    {
      title: 'Output',
      key: 'output',
      render: (_, row) => priceInput(row, 'output', 'Output price'),
    },
    {
      title: 'Cached input',
      key: 'cached',
      render: (_, row) => priceInput(row, 'cached', 'Cached input price'),
    },
    { title: 'Seen in 24 h', key: 'seen', render: (_, row) => seenText(row.seenCalls) },
    {
      title: '',
      key: 'actions',
      width: 90,
      render: (_, row) =>
        canManage && (
          <div className="flex gap-1">
            <Tooltip title="Save">
              <Button
                type="text"
                size="small"
                aria-label={`Save price for ${row.model || 'new model'}`}
                icon={<CheckOutlined />}
                disabled={!row.isDirty || !row.model.trim()}
                loading={savingKey === row.key}
                onClick={() => save(row)}
              />
            </Tooltip>
            {(row.id || row.key.startsWith('new:')) && (
              <Popconfirm
                title={row.id ? `Delete the price for ${row.model}?` : 'Remove this row?'}
                okText="Delete"
                okButtonProps={{ danger: true }}
                onConfirm={() => remove(row)}
              >
                <Tooltip title="Delete">
                  <Button
                    type="text"
                    size="small"
                    danger
                    aria-label={`Delete price for ${row.model || 'new model'}`}
                    icon={<DeleteOutlined />}
                  />
                </Tooltip>
              </Popconfirm>
            )}
          </div>
        ),
    },
  ];

  return (
    <div className="rounded-lg border border-gray-200 bg-white p-4 dark:border-gray-700 dark:bg-gray-800">
      <div className="mb-3 flex flex-wrap items-start gap-3">
        <div className="mr-auto max-w-2xl">
          <h3 className="m-0 text-base font-semibold text-gray-900 dark:text-gray-100">
            Model prices
          </h3>
          <p className="m-0 text-sm text-gray-600 dark:text-gray-300">
            USD per million tokens, for LLM apps&apos; cost estimates and budgets. Claude Code
            reports its own cost and doesn&apos;t use this table. Obzly doesn&apos;t fetch prices:
            check them against your provider.
          </p>
        </div>
        {canManage && (
          <div className="flex gap-2">
            <Button onClick={fillCommon}>Fill in common models</Button>
            <Button icon={<PlusOutlined />} onClick={addRow}>
              Add model
            </Button>
          </div>
        )}
      </div>
      {filledAsOf && (
        <Alert
          className="!mb-3"
          type="warning"
          showIcon
          title={`Added a starting list dated ${filledAsOf}. Prices change: review each one before relying on budgets.`}
        />
      )}
      {seenError && <Alert className="!mb-3" type="info" showIcon title={seenError} />}
      {isLoading ? (
        <div className="flex justify-center p-8">
          <Spin indicator={<LoadingOutlined spin />} />
        </div>
      ) : (
        <Table<PriceRow>
          size="small"
          rowKey="key"
          columns={columns}
          dataSource={rows}
          pagination={false}
          scroll={{ x: 'max-content' }}
          locale={{ emptyText: 'No prices yet, and no LLM calls in the last 24 hours' }}
        />
      )}
    </div>
  );
};
