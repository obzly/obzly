import { DeleteOutlined, PlusOutlined } from '@ant-design/icons';
import { Button, Input, InputNumber, Select } from 'antd';

import type { Assertion, AssertionKind, AssertionOp } from '../../../entity/synthetics';

const KIND_LABEL: Record<AssertionKind, string> = {
  status: 'Status code',
  response_time_ms: 'Response time (ms)',
  header: 'Header',
  json: 'JSON path',
  body: 'Body',
};
const OPS: Record<AssertionKind, AssertionOp[]> = {
  status: ['equals', 'in'],
  response_time_ms: ['below'],
  header: ['equals', 'contains', 'exists'],
  json: ['equals', 'contains', 'exists'],
  body: ['contains', 'matches'],
};

export const Field = ({
  label,
  hint,
  children,
}: {
  label: string;
  hint?: string;
  children: React.ReactNode;
}) => (
  <div className="flex min-w-0 flex-col gap-1">
    <span className="text-xs font-medium text-gray-600 dark:text-gray-300">{label}</span>
    {children}
    {hint && <span className="text-[11px] text-gray-500 dark:text-gray-400">{hint}</span>}
  </div>
);

/** An editable list of assertions, each fail or warn. */
export const AssertionsEditor = ({
  assertions,
  kinds,
  onChange,
}: {
  assertions: Assertion[];
  kinds: AssertionKind[];
  onChange: (assertions: Assertion[]) => void;
}) => {
  const updateAssertion = (index: number, change: Partial<Assertion>) =>
    onChange(assertions.map((item, i) => (i === index ? { ...item, ...change } : item)));
  return (
    <div className="flex flex-col gap-2">
      {assertions.map((assertion, index) => (
        <div
          key={index}
          className="grid grid-cols-2 gap-2 sm:grid-cols-[150px_100px_minmax(0,1fr)_minmax(0,1fr)_84px_32px]"
        >
          <Select
            aria-label="Assertion"
            value={assertion.kind}
            options={kinds.map((kind) => ({ value: kind, label: KIND_LABEL[kind] }))}
            onChange={(kind: AssertionKind) =>
              updateAssertion(index, {
                kind,
                op: OPS[kind][0],
                name: undefined,
                path: kind === 'json' ? '$.' : undefined,
                value: undefined,
                values: undefined,
                number: undefined,
              })
            }
          />
          <Select
            aria-label="Operator"
            value={assertion.op}
            options={OPS[assertion.kind].map((op) => ({ value: op, label: op }))}
            onChange={(op: AssertionOp) => updateAssertion(index, { op })}
          />
          {assertion.kind === 'header' || assertion.kind === 'json' ? (
            <Input
              className="font-mono"
              placeholder={assertion.kind === 'json' ? '$.status' : 'Content-Type'}
              value={assertion.kind === 'json' ? assertion.path : assertion.name}
              onChange={(event) =>
                updateAssertion(
                  index,
                  assertion.kind === 'json'
                    ? { path: event.target.value }
                    : { name: event.target.value },
                )
              }
            />
          ) : (
            <span className="hidden sm:block" />
          )}
          <AssertionValue
            assertion={assertion}
            onChange={(change) => updateAssertion(index, change)}
          />
          <Select
            aria-label="Severity"
            value={assertion.severity ?? 'fail'}
            options={[
              { value: 'fail', label: 'fail' },
              { value: 'warn', label: 'warn' },
            ]}
            onChange={(severity) => updateAssertion(index, { severity })}
          />
          <Button
            type="text"
            icon={<DeleteOutlined />}
            aria-label="Remove assertion"
            onClick={() => onChange(assertions.filter((_, i) => i !== index))}
          />
        </div>
      ))}
      <Button
        className="self-start"
        size="small"
        icon={<PlusOutlined />}
        onClick={() =>
          onChange([
            ...assertions,
            { kind: 'response_time_ms', op: 'below', number: 800, severity: 'fail' },
          ])
        }
      >
        Add assertion
      </Button>
    </div>
  );
};

const AssertionValue = ({
  assertion,
  onChange,
}: {
  assertion: Assertion;
  onChange: (change: Partial<Assertion>) => void;
}) => {
  if (assertion.op === 'exists') return <span className="hidden sm:block" />;
  if (assertion.op === 'below') {
    return (
      <InputNumber
        className="w-full"
        min={1}
        value={assertion.number}
        suffix="ms"
        onChange={(number) => onChange({ number: number ?? undefined })}
      />
    );
  }
  if (assertion.op === 'in') {
    return (
      <Select
        mode="tags"
        tokenSeparators={[',', ' ']}
        value={(assertion.values ?? []).map(String)}
        onChange={(values: string[]) =>
          onChange({ values: values.map(Number).filter((code) => Number.isInteger(code)) })
        }
      />
    );
  }
  return (
    <Input
      className="font-mono"
      value={assertion.value ?? ''}
      placeholder={assertion.kind === 'status' ? '200' : 'ok'}
      onChange={(event) => onChange({ value: event.target.value })}
    />
  );
};
