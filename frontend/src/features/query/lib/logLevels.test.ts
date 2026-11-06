import { describe, expect, it } from 'vitest';

import type { QueryNode } from '../../../entity/query';
import { OTHER_LEVEL_COLOR, levelColor, sortLevels, withHiddenLevels } from './logLevels';

describe('log levels', () => {
  it('sorts known levels by severity, then others alphabetically', () => {
    expect(sortLevels(['info', 'zeta', 'ERROR', 'alpha', 'debug', 'info'])).toEqual([
      'ERROR',
      'info',
      'debug',
      'alpha',
      'zeta',
    ]);
  });

  it('colors unknown levels gray', () => {
    expect(levelColor('custom')).toBe(OTHER_LEVEL_COLOR);
    expect(levelColor('ERROR')).toBe(levelColor('error'));
  });

  it('ANDs hidden levels onto the query, unchanged when none are hidden', () => {
    const base = {
      type: 'condition',
      condition: { field: 'message', operator: 'contains', value: 'x' },
    } as QueryNode;
    expect(withHiddenLevels(base, new Set())).toBe(base);
    expect(withHiddenLevels(null, new Set(['info', 'debug']))).toEqual({
      type: 'condition',
      condition: { field: 'level', operator: 'not_in', value: ['debug', 'info'] },
    });
    expect(withHiddenLevels(base, new Set(['info']))?.logic?.operator).toBe('and');
  });
});
