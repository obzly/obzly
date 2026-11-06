import {
  ArrowDownOutlined,
  ArrowUpOutlined,
  CopyOutlined,
  DeleteOutlined,
  DownOutlined,
  PlusOutlined,
  RightOutlined,
} from '@ant-design/icons';
import { Alert, Button, Checkbox, Input, InputNumber, Select, Tag } from 'antd';
import { useMemo, useState } from 'react';

import type { Definition, Extract, ExtractSource, Step } from '../../../entity/synthetics';
import {
  EXTRACT_LABEL,
  MAX_STEPS,
  duplicateStep,
  emptyStep,
  moveStep,
  stepSummary,
  usageText,
  variableReport,
} from '../lib/multiStep';
import { HTTP_ASSERTIONS, METHODS } from '../lib/requests';
import { AssertionsEditor, Field } from './editorParts';
import { IconAction, Panel } from './parts';

interface Props {
  definition: Definition;
  onChange: (change: Partial<Definition>) => void;
}

const METHOD_COLOR: Record<string, string> = {
  GET: 'blue',
  HEAD: 'default',
  POST: 'green',
  PUT: 'orange',
  PATCH: 'gold',
  DELETE: 'red',
  OPTIONS: 'default',
};
const BODY_METHODS = ['POST', 'PUT', 'PATCH', 'DELETE'];
const SOURCES = Object.entries(EXTRACT_LABEL).map(([value, label]) => ({ value, label }));
const PATH_PLACEHOLDER: Partial<Record<ExtractSource, string>> = {
  json: '$.accessToken',
  header: 'Location',
  regex: '"id":"(\\w+)"',
};

/** The steps of a multi-step check: a collapsed line each, one open for editing. */
export const MultiStepEditor = ({ definition, onChange }: Props) => {
  const [open, setOpen] = useState<number | undefined>(0);
  const steps = definition.steps ?? [];
  const undefinedByStep = useMemo(() => {
    const byStep = new Map<number, string[]>();
    for (const use of variableReport(definition).undefinedUses) {
      byStep.set(use.step, [...(byStep.get(use.step) ?? []), use.name]);
    }
    return byStep;
  }, [definition]);

  const setSteps = (next: Step[]) => onChange({ steps: next });
  const updateStep = (index: number, change: Partial<Step>) =>
    setSteps(steps.map((step, i) => (i === index ? { ...step, ...change } : step)));

  return (
    <div className="flex flex-col gap-2">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <span className="text-sm font-semibold">
          Steps{' '}
          <span className="font-normal text-gray-500 dark:text-gray-400">
            · {steps.length} of {MAX_STEPS}
          </span>
        </span>
        <Checkbox
          checked={definition.cookies !== false}
          onChange={(event) => onChange({ cookies: event.target.checked ? undefined : false })}
        >
          Keep cookies between steps
        </Checkbox>
      </div>
      {steps.map((step, index) => (
        <StepCard
          key={index}
          step={step}
          index={index}
          count={steps.length}
          open={open === index}
          totalTimeout={definition.timeoutSeconds}
          undefinedNames={undefinedByStep.get(index + 1) ?? []}
          onToggle={() => setOpen(open === index ? undefined : index)}
          onChange={(change) => updateStep(index, change)}
          onMove={(by) => {
            setSteps(moveStep(steps, index, by));
            if (open === index) setOpen(index + by);
          }}
          onDuplicate={() => {
            setSteps(duplicateStep(steps, index));
            setOpen(index + 1);
          }}
          onDelete={() => {
            setSteps(steps.filter((_, i) => i !== index));
            setOpen(undefined);
          }}
        />
      ))}
      <Button
        type="dashed"
        icon={<PlusOutlined />}
        disabled={steps.length >= MAX_STEPS}
        onClick={() => {
          setSteps([...steps, emptyStep(steps.length + 1)]);
          setOpen(steps.length);
        }}
      >
        Add step
      </Button>
    </div>
  );
};

const StepCard = ({
  step,
  index,
  count,
  open,
  totalTimeout,
  undefinedNames,
  onToggle,
  onChange,
  onMove,
  onDuplicate,
  onDelete,
}: {
  step: Step;
  index: number;
  count: number;
  open: boolean;
  totalTimeout: number;
  undefinedNames: string[];
  onToggle: () => void;
  onChange: (change: Partial<Step>) => void;
  onMove: (by: -1 | 1) => void;
  onDuplicate: () => void;
  onDelete: () => void;
}) => {
  const request = step.request;
  const headers = Object.entries(request.headers ?? {});
  const extracts = step.extract ?? [];
  const updateRequest = (change: Partial<Step['request']>) =>
    onChange({ request: { ...request, ...change } });
  const setHeaders = (rows: [string, string][]) =>
    updateRequest({ headers: rows.length ? Object.fromEntries(rows) : undefined });
  const updateExtract = (i: number, change: Partial<Extract>) =>
    onChange({ extract: extracts.map((item, j) => (j === i ? { ...item, ...change } : item)) });

  return (
    <div
      className={`rounded-lg border bg-white dark:bg-gray-800 ${open ? 'border-emerald-600' : 'border-gray-200 dark:border-gray-700'}`}
    >
      <div className="flex flex-wrap items-center gap-2 px-3 py-2">
        <span className="flex h-6 w-6 shrink-0 items-center justify-center rounded-full bg-gray-100 text-xs font-semibold text-gray-700 dark:bg-gray-700 dark:text-gray-200">
          {index + 1}
        </span>
        <Tag color={METHOD_COLOR[request.method] ?? 'default'} className="m-0 font-mono">
          {request.method}
        </Tag>
        <button
          type="button"
          aria-expanded={open}
          className="flex min-w-0 flex-[1_1_240px] cursor-pointer flex-col items-start gap-0.5 border-0 bg-transparent p-0 text-left"
          onClick={onToggle}
        >
          <span className="flex items-center gap-1.5 font-semibold text-gray-900 dark:text-gray-100">
            {open ? (
              <DownOutlined className="text-[10px]" />
            ) : (
              <RightOutlined className="text-[10px]" />
            )}
            {step.name || `Step ${index + 1}`}
          </span>
          <span className="w-full truncate font-mono text-xs text-gray-500 dark:text-gray-400">
            {stepSummary(step)}
          </span>
        </button>
        {step.continueOnFailure && <Tag color="blue">runs even after a failure</Tag>}
        {undefinedNames.length > 0 && (
          <Tag color="error">
            {undefinedNames.map((name) => `{{${name}}}`).join(', ')} undefined
          </Tag>
        )}
        <span className="inline-flex gap-0.5">
          <IconAction
            title="Move up"
            icon={<ArrowUpOutlined />}
            disabled={index === 0}
            onClick={() => onMove(-1)}
          />
          <IconAction
            title="Move down"
            icon={<ArrowDownOutlined />}
            disabled={index === count - 1}
            onClick={() => onMove(1)}
          />
          <IconAction
            title="Duplicate step"
            icon={<CopyOutlined />}
            disabled={count >= MAX_STEPS}
            onClick={onDuplicate}
          />
          <IconAction
            title="Delete step"
            icon={<DeleteOutlined />}
            danger
            disabled={count === 1}
            onClick={onDelete}
          />
        </span>
      </div>

      {open && (
        <div className="flex flex-col gap-3 border-t border-gray-200 p-3 dark:border-gray-700">
          <Field label="Step name">
            <Input value={step.name} onChange={(event) => onChange({ name: event.target.value })} />
          </Field>
          <div className="grid gap-3 sm:grid-cols-[120px_minmax(0,1fr)]">
            <Field label="Method">
              <Select
                value={request.method}
                options={METHODS.map((method) => ({ value: method, label: method }))}
                onChange={(method) => updateRequest({ method })}
              />
            </Field>
            <Field label="URL" hint="{{name}} uses a variable or a value an earlier step extracted">
              <Input
                className="font-mono"
                value={request.url}
                onChange={(event) => updateRequest({ url: event.target.value })}
              />
            </Field>
          </div>
          <Field label="Headers">
            <div className="flex flex-col gap-2">
              {headers.map(([name, value], i) => (
                <div key={i} className="grid grid-cols-[minmax(0,2fr)_minmax(0,3fr)_32px] gap-2">
                  <Input
                    aria-label="Header name"
                    placeholder="Authorization"
                    value={name}
                    onChange={(event) =>
                      setHeaders(
                        headers.map((row, j) => (j === i ? [event.target.value, row[1]] : row)),
                      )
                    }
                  />
                  <Input
                    aria-label="Header value"
                    className="font-mono"
                    placeholder="Bearer {{token}}"
                    value={value}
                    onChange={(event) =>
                      setHeaders(
                        headers.map((row, j) => (j === i ? [row[0], event.target.value] : row)),
                      )
                    }
                  />
                  <Button
                    type="text"
                    icon={<DeleteOutlined />}
                    aria-label="Remove header"
                    onClick={() => setHeaders(headers.filter((_, j) => j !== i))}
                  />
                </div>
              ))}
              <Button
                className="self-start"
                size="small"
                icon={<PlusOutlined />}
                // One blank row at a time: header names are the keys.
                disabled={headers.some(([name]) => name === '')}
                onClick={() => setHeaders([...headers, ['', '']])}
              >
                Add header
              </Button>
            </div>
          </Field>
          {BODY_METHODS.includes(request.method) && (
            <Field label="Body">
              <Input.TextArea
                className="font-mono"
                rows={3}
                value={request.body ?? ''}
                onChange={(event) => updateRequest({ body: event.target.value || undefined })}
              />
            </Field>
          )}
          <Field
            label="Assertions"
            hint="Values may use {{name}}. A warn failure never stops the run."
          >
            <AssertionsEditor
              assertions={step.assertions ?? []}
              kinds={HTTP_ASSERTIONS}
              onChange={(assertions) => onChange({ assertions })}
            />
          </Field>
          <Field label="Extract for later steps">
            <div className="flex flex-col gap-2">
              {extracts.map((extract, i) => (
                <div
                  key={i}
                  className="grid grid-cols-2 items-center gap-2 sm:grid-cols-[minmax(0,1fr)_150px_minmax(0,1.4fr)_80px_32px]"
                >
                  <Input
                    aria-label="Variable name"
                    className="font-mono"
                    placeholder="token"
                    value={extract.name}
                    onChange={(event) => updateExtract(i, { name: event.target.value })}
                  />
                  <Select
                    aria-label="From"
                    value={extract.from}
                    options={SOURCES}
                    onChange={(from: ExtractSource) =>
                      updateExtract(i, {
                        from,
                        path:
                          from === 'json'
                            ? '$.'
                            : from === 'status' || from === 'body'
                              ? undefined
                              : '',
                      })
                    }
                  />
                  {extract.from === 'status' || extract.from === 'body' ? (
                    <span className="hidden sm:block" />
                  ) : (
                    <Input
                      aria-label="Path"
                      className="font-mono"
                      placeholder={PATH_PLACEHOLDER[extract.from]}
                      value={extract.path ?? ''}
                      onChange={(event) => updateExtract(i, { path: event.target.value })}
                    />
                  )}
                  <Checkbox
                    checked={!!extract.secret}
                    onChange={(event) => updateExtract(i, { secret: event.target.checked })}
                  >
                    Secret
                  </Checkbox>
                  <Button
                    type="text"
                    icon={<DeleteOutlined />}
                    aria-label="Remove extracted value"
                    onClick={() => onChange({ extract: extracts.filter((_, j) => j !== i) })}
                  />
                </div>
              ))}
              <Button
                className="self-start"
                size="small"
                icon={<PlusOutlined />}
                onClick={() =>
                  onChange({ extract: [...extracts, { name: '', from: 'json', path: '$.' }] })
                }
              >
                Extract a value
              </Button>
            </div>
          </Field>
          <div className="flex flex-wrap items-center gap-6">
            <Checkbox
              checked={!!step.continueOnFailure}
              onChange={(event) =>
                onChange({ continueOnFailure: event.target.checked || undefined })
              }
            >
              Run even if an earlier step failed (clean-up)
            </Checkbox>
            <label className="flex items-center gap-2 text-sm">
              Step timeout
              <InputNumber
                size="small"
                min={1}
                max={totalTimeout}
                suffix="s"
                value={step.timeoutSeconds ?? Math.min(10, totalTimeout)}
                onChange={(timeoutSeconds) =>
                  onChange({ timeoutSeconds: timeoutSeconds ?? undefined })
                }
              />
            </label>
          </div>
        </div>
      )}
    </div>
  );
};

/** Monitor variables to edit, what each step extracts, where each is used, and names nothing defines. */
export const VariablesPanel = ({ definition, onChange }: Props) => {
  const { rows, undefinedUses } = useMemo(() => variableReport(definition), [definition]);
  const variables = Object.entries(definition.variables ?? {});
  const setVariables = (entries: [string, string][]) =>
    onChange({ variables: Object.fromEntries(entries) });

  return (
    <Panel
      title="Variables"
      subtitle="Use {{name}} in a later step's URL, headers, body or assertions. ${env:NAME} values are secrets."
    >
      <div className="flex flex-col gap-2">
        {variables.map(([name, value], i) => {
          const row = rows.find((item) => item.name === name && item.step === undefined);
          return (
            <div key={i} className="flex flex-col gap-1">
              <div className="grid grid-cols-[minmax(0,1fr)_minmax(0,1.6fr)_32px] gap-2">
                <Input
                  aria-label="Variable name"
                  className="font-mono"
                  placeholder="user"
                  value={name}
                  onChange={(event) =>
                    setVariables(
                      variables.map((entry, j) =>
                        j === i ? [event.target.value, entry[1]] : entry,
                      ),
                    )
                  }
                />
                <Input
                  aria-label="Variable value"
                  className="font-mono"
                  placeholder="synthetic@example.com or ${env:NAME}"
                  value={value}
                  onChange={(event) =>
                    setVariables(
                      variables.map((entry, j) =>
                        j === i ? [entry[0], event.target.value] : entry,
                      ),
                    )
                  }
                />
                <Button
                  type="text"
                  icon={<DeleteOutlined />}
                  aria-label="Remove variable"
                  onClick={() => setVariables(variables.filter((_, j) => j !== i))}
                />
              </div>
              {name && (
                <span className="flex items-center gap-2 text-[11px] text-gray-500 dark:text-gray-400">
                  {usageText(row?.usedIn ?? [])}
                  {row?.secret && <Tag className="m-0 text-[10px]">secret</Tag>}
                </span>
              )}
            </div>
          );
        })}
        <Button
          className="self-start"
          size="small"
          icon={<PlusOutlined />}
          disabled={variables.some(([name]) => name === '')}
          onClick={() => setVariables([...variables, ['', '']])}
        >
          Add variable
        </Button>

        {rows
          .filter((row) => row.step !== undefined)
          .map((row) => (
            <div
              key={`${row.step}-${row.name}`}
              className="flex items-start gap-2 border-t border-gray-100 pt-2 dark:border-gray-700"
            >
              <code className="w-24 shrink-0 truncate font-mono text-xs font-medium text-emerald-700 dark:text-emerald-400">
                {row.name}
              </code>
              <span className="flex min-w-0 flex-col text-xs text-gray-500 dark:text-gray-400">
                <span className="text-gray-900 dark:text-gray-100">{row.source}</span>
                <span>{usageText(row.usedIn)}</span>
              </span>
              {row.secret && <Tag className="m-0 ml-auto text-[10px]">secret</Tag>}
            </div>
          ))}

        {undefinedUses.map((use) => (
          <Alert
            key={`${use.step}-${use.name}`}
            type="error"
            showIcon
            title={
              <span>
                Step {use.step} uses <code className="font-mono">{`{{${use.name}}}`}</code>, which
                no earlier step or variable defines.
              </span>
            }
          />
        ))}
      </div>
    </Panel>
  );
};
