import { describe, expect, it } from 'vitest';

import { DEFAULT_TRACE_VIEW_SETTINGS, parseTraceViewSettings } from './traceViewSettings';

describe('parseTraceViewSettings', () => {
  it('returns the defaults for nothing stored or broken JSON', () => {
    expect(parseTraceViewSettings(null)).toEqual(DEFAULT_TRACE_VIEW_SETTINGS);
    expect(parseTraceViewSettings('{not json')).toEqual(DEFAULT_TRACE_VIEW_SETTINGS);
  });

  it('keeps valid values and drops unknown ones', () => {
    const parsed = parseTraceViewSettings(
      JSON.stringify({
        preset: 'focus',
        detail: 'sideways',
        detailWidth: 'l',
        textSize: 16,
        wrap: true,
        columns: ['errors', 'trace', 'bogus'],
      }),
    );
    expect(parsed.preset).toBe('focus');
    expect(parsed.detail).toBe(DEFAULT_TRACE_VIEW_SETTINGS.detail);
    expect(parsed.detailWidth).toBe('l');
    expect(parsed.textSize).toBe(DEFAULT_TRACE_VIEW_SETTINGS.textSize);
    expect(parsed.wrap).toBe(true);
    // Columns come back in table order.
    expect(parsed.columns).toEqual(['trace', 'errors']);
  });

  it('never leaves the table without columns', () => {
    expect(parseTraceViewSettings(JSON.stringify({ columns: [] })).columns).toEqual(
      DEFAULT_TRACE_VIEW_SETTINGS.columns,
    );
  });
});
