import { describe, expect, it } from 'vitest';

import type { QueryNode } from '../../../entity/query';
import {
  SearchQuerySyntaxError,
  parseSearchQuery,
  splitTopLevelAndClauses,
} from './searchQueryParser';

const cond = (field: string, operator: string, value: unknown): QueryNode =>
  ({ type: 'condition', condition: { field, operator, value } }) as QueryNode;
const logic = (operator: string, ...children: QueryNode[]): QueryNode =>
  ({ type: 'logical', logic: { operator, children } }) as QueryNode;

describe('parseSearchQuery', () => {
  it('matches everything on an empty search', () => {
    expect(parseSearchQuery('')).toBeNull();
    expect(parseSearchQuery('   ')).toBeNull();
  });

  it('searches the message for a bare word', () => {
    expect(parseSearchQuery('timeout')).toEqual(cond('message', 'contains', 'timeout'));
  });

  it('maps each operator', () => {
    expect(parseSearchQuery('service:checkout')).toEqual(cond('service', 'contains', 'checkout'));
    expect(parseSearchQuery('level=error')).toEqual(cond('level', 'equals', 'error'));
    expect(parseSearchQuery('level!=debug')).toEqual(cond('level', 'not_equals', 'debug'));
    expect(parseSearchQuery('trace_id:*')).toEqual(cond('trace_id', 'exists', null));
  });

  it('compares numbers numerically (promoted number fields)', () => {
    expect(parseSearchQuery('duration_ms>100')).toEqual(cond('duration_ms', 'greater_than', 100));
    expect(parseSearchQuery('duration_ms>=100')).toEqual(
      cond('duration_ms', 'greater_or_equal', 100),
    );
    expect(parseSearchQuery('duration_ms<5')).toEqual(cond('duration_ms', 'less_than', 5));
    expect(parseSearchQuery('duration_ms<=5')).toEqual(cond('duration_ms', 'less_or_equal', 5));
  });

  it('keeps a quoted phrase whole', () => {
    expect(parseSearchQuery('message:"connection reset by peer"')).toEqual(
      cond('message', 'contains', 'connection reset by peer'),
    );
  });

  it('applies NOT before AND before OR, with implicit AND', () => {
    expect(parseSearchQuery('a OR b c')).toEqual(
      logic(
        'or',
        cond('message', 'contains', 'a'),
        logic('and', cond('message', 'contains', 'b'), cond('message', 'contains', 'c')),
      ),
    );
    expect(parseSearchQuery('NOT level=debug')).toEqual(
      logic('not', cond('level', 'equals', 'debug')),
    );
    expect(parseSearchQuery('-level=debug')).toEqual(
      logic('not', cond('level', 'equals', 'debug')),
    );
  });

  it('groups with parentheses', () => {
    expect(parseSearchQuery('(a OR b) AND level=error')).toEqual(
      logic(
        'and',
        logic('or', cond('message', 'contains', 'a'), cond('message', 'contains', 'b')),
        cond('level', 'equals', 'error'),
      ),
    );
  });

  it('reports unbalanced parentheses as a syntax error', () => {
    expect(() => parseSearchQuery('(a OR b')).toThrow(SearchQuerySyntaxError);
    expect(() => parseSearchQuery('a OR b)')).toThrow(SearchQuerySyntaxError);
  });
});

// Click-to-filter toggles a clause off only in the flat AND shape it builds.
describe('splitTopLevelAndClauses', () => {
  it('splits a flat AND chain into clauses', () => {
    expect(splitTopLevelAndClauses('level=error AND service:checkout')).toEqual([
      'level=error',
      'service:checkout',
    ]);
    expect(splitTopLevelAndClauses('')).toEqual([]);
  });

  it('refuses anything else, so toggling never changes the logic', () => {
    expect(splitTopLevelAndClauses('level=error AND (a OR b)')).toBeNull();
    expect(splitTopLevelAndClauses('NOT level=debug')).toBeNull();
    expect(splitTopLevelAndClauses('AND level=error')).toBeNull();
  });
});
