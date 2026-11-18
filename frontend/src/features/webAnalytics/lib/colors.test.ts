import { describe, expect, it } from 'vitest';

import { statusColor, withAlpha } from './colors';

const status = { ok: 'ok', warn: 'warn', bad: 'bad', info: 'info', noData: 'none' };

describe('statusColor', () => {
  it('colours status classes by meaning', () => {
    expect(statusColor('200', status)).toBe('ok');
    expect(statusColor('304', status)).toBe('info');
    expect(statusColor('404', status)).toBe('warn');
    expect(statusColor('502', status)).toBe('bad');
    expect(statusColor('101', status)).toBe('none');
  });
});

describe('withAlpha', () => {
  it('tints a hex colour and leaves anything else alone', () => {
    expect(withAlpha('#10b981', 0.5)).toBe('rgba(16, 185, 129, 0.5)');
    expect(withAlpha('var(--v2-accent)', 0.5)).toBe('var(--v2-accent)');
  });
});
