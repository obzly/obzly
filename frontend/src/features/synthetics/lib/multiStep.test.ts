import { describe, expect, it } from 'vitest';

import type { Definition, RunResult, Step } from '../../../entity/synthetics';
import {
  cleanMultiDefinition,
  duplicateStep,
  failedStepNote,
  moveStep,
  stepRows,
  stepSummary,
  usageText,
  usedVariables,
  variableReport,
} from './multiStep';

const signIn: Step = {
  name: 'Sign in',
  request: {
    method: 'POST',
    url: 'https://api.example.com/auth/login',
    body: '{"email":"{{user}}","password":"{{ password }}"}',
  },
  assertions: [
    { kind: 'status', op: 'equals', value: '200', severity: 'fail' },
    { kind: 'response_time_ms', op: 'below', number: 800, severity: 'warn' },
  ],
  extract: [{ name: 'token', from: 'json', path: '$.accessToken', secret: true }],
};
const createOrder: Step = {
  name: 'Create order',
  request: {
    method: 'POST',
    url: 'https://api.example.com/orders?id={{$uuid}}',
    headers: { Authorization: 'Bearer {{token}}' },
  },
  assertions: [{ kind: 'json', op: 'equals', path: '$.user', value: '{{user}}' }],
  extract: [{ name: 'orderId', from: 'json', path: '$.id' }],
};
const cancel: Step = {
  name: 'Cancel order',
  continueOnFailure: true,
  request: {
    method: 'DELETE',
    url: 'https://api.example.com/orders/{{orderId}}?cart={{cartId}}',
    headers: { Authorization: 'Bearer {{token}}' },
  },
};
const definition: Definition = {
  type: 'multi',
  timeoutSeconds: 60,
  variables: { user: 'synthetic@example.com', password: '${env:OBZLY_SECRET_CHECKOUT_PASSWORD}' },
  steps: [signIn, createOrder, cancel],
};

describe('usedVariables', () => {
  it('finds names in the URL, header values, body and assertion values, built-ins included', () => {
    expect(usedVariables(signIn)).toEqual(['user', 'password']);
    expect(usedVariables(createOrder)).toEqual(['$uuid', 'token', 'user']);
  });
});

describe('variableReport', () => {
  it('lists sources and usage, marks secrets and flags names nothing earlier defines', () => {
    const { rows, undefinedUses } = variableReport(definition);

    expect(rows).toEqual([
      {
        name: 'user',
        source: 'Variable: synthetic@example.com',
        usedIn: [1, 2],
        secret: false,
      },
      {
        name: 'password',
        source: 'Variable: ${env:OBZLY_SECRET_CHECKOUT_PASSWORD}',
        usedIn: [1],
        secret: true,
      },
      {
        name: 'token',
        source: 'Step 1 · JSON path $.accessToken',
        usedIn: [2, 3],
        secret: true,
        step: 1,
      },
      { name: 'orderId', source: 'Step 2 · JSON path $.id', usedIn: [3], secret: false, step: 2 },
    ]);
    expect(undefinedUses).toEqual([{ step: 3, name: 'cartId' }]);
  });

  it('flags a value used before the step that extracts it', () => {
    const { undefinedUses } = variableReport({ ...definition, steps: [cancel, createOrder] });

    expect(undefinedUses).toEqual([
      { step: 1, name: 'orderId' },
      { step: 1, name: 'cartId' },
      { step: 1, name: 'token' },
      { step: 2, name: 'token' },
    ]);
  });
});

describe('step helpers', () => {
  it('summarizes a step on one line', () => {
    expect(stepSummary(signIn)).toBe(
      'POST https://api.example.com/auth/login · 2 assertions · extracts token',
    );
    expect(stepSummary(cancel)).toBe(
      'DELETE https://api.example.com/orders/{{orderId}}?cart={{cartId}}',
    );
    expect(usageText([2, 3])).toBe('Used in steps 2, 3');
    expect(usageText([])).toBe('Not used yet');
  });

  it('moves, duplicates and stops at the ends and at ten steps', () => {
    const steps = [signIn, createOrder, cancel];

    expect(moveStep(steps, 0, 1).map((step) => step.name)).toEqual([
      'Create order',
      'Sign in',
      'Cancel order',
    ]);
    expect(moveStep(steps, 0, -1)).toBe(steps);
    expect(duplicateStep(steps, 1).map((step) => step.name)).toEqual([
      'Sign in',
      'Create order',
      'Create order (copy)',
      'Cancel order',
    ]);
    expect(duplicateStep(Array(10).fill(signIn), 0)).toHaveLength(10);
  });

  it('drops blank header, variable and extract rows before saving', () => {
    const cleaned = cleanMultiDefinition({
      ...definition,
      variables: { '': 'x', user: 'a' },
      steps: [
        {
          ...signIn,
          request: { ...signIn.request, headers: { '': 'y' } },
          extract: [{ name: ' ', from: 'body' }],
        },
      ],
    });

    expect(cleaned.variables).toEqual({ user: 'a' });
    expect(cleaned.steps?.[0].request.headers).toBeUndefined();
    expect(cleaned.steps?.[0].extract).toEqual([]);
  });
});

describe('stepRows', () => {
  const series = (index: number, step: string, value: number) => ({
    labels: { step_index: String(index), step },
    points: [{ timestamp: '2026-10-27T10:00:00Z', value }],
  });

  it('fills each current step, keeps an old index still in range, and splits run time by p95', () => {
    const rows = stepRows(
      [signIn, createOrder],
      [series(1, 'Sign in', 1), series(2, 'Create order', 0.989), series(3, 'Cancel order', 0.996)],
      [series(1, 'Sign in', 0.17), series(2, 'Create order', 0.23)],
      [series(1, 'Sign in', 0.2), series(2, 'Create order', 0.4), series(3, 'Cancel order', 0.4)],
    );

    expect(rows.map((row) => [row.index, row.name, row.success, row.share])).toEqual([
      [1, 'Sign in', 1, 0.2],
      [2, 'Create order', 0.989, 0.4],
      [3, 'Cancel order', 0.996, 0.4],
    ]);
    expect(rows[0].p50Ms).toBeCloseTo(170);
    expect(rows[2].p50Ms).toBeUndefined();
  });

  it('lists the steps with no data yet', () => {
    expect(stepRows([signIn], [], [], []).map((row) => [row.name, row.share])).toEqual([
      ['Sign in', undefined],
    ]);
  });
});

describe('failedStepNote', () => {
  it('names the failing step and why', () => {
    const run = {
      status: 'down',
      steps: [
        { index: 1, name: 'Sign in', status: 'up', httpStatus: 200 },
        { index: 2, name: 'Create order', status: 'down', httpStatus: 409 },
        { index: 3, name: 'Cancel order', status: 'skipped' },
      ],
    } as RunResult;

    expect(failedStepNote(run)).toBe('failed at Create order (409)');
    expect(failedStepNote({ status: 'up' } as RunResult)).toBe('');
  });
});
