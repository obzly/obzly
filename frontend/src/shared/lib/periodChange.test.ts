import { describe, expect, it } from 'vitest';

import { absoluteChange, relativeChange } from './periodChange';

describe('relativeChange', () => {
  it('colours by good or bad, not by sign', () => {
    expect(relativeChange(112, 100, 'up')).toEqual({ text: '+12%', tone: 'good' });
    expect(relativeChange(112, 100, 'down')).toEqual({ text: '+12%', tone: 'bad' });
    expect(relativeChange(95, 100, 'down')).toEqual({ text: '-5.0%', tone: 'good' });
    expect(relativeChange(95, 100, 'neither')).toEqual({ text: '-5.0%', tone: 'neutral' });
  });

  it('shows nothing without an earlier value to compare with', () => {
    expect(relativeChange(10, undefined, 'up')).toBeUndefined();
    expect(relativeChange(undefined, 10, 'up')).toBeUndefined();
    expect(relativeChange(10, 0, 'up')).toBeUndefined();
    expect(relativeChange(0, 0, 'up')).toEqual({ text: '0%', tone: 'neutral' });
  });
});

describe('absoluteChange', () => {
  it('formats the difference', () => {
    expect(absoluteChange(5, 4, 'neither')).toEqual({ text: '+1', tone: 'neutral' });
    expect(absoluteChange(3.4, 3.8, 'down', (d) => `${d.toFixed(1)} s`)).toEqual({
      text: '-0.4 s',
      tone: 'good',
    });
    expect(absoluteChange(2, 2, 'down')).toEqual({ text: '±0', tone: 'neutral' });
    expect(absoluteChange(2, null, 'down')).toBeUndefined();
  });
});
