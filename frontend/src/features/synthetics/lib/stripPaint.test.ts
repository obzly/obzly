import { describe, expect, it } from 'vitest';

import { SLOT_COLOR, slotAt, stripBackground, stripSummary } from './stripPaint';

describe('stripPaint', () => {
  it('Test_StripBackground_OneStopPerSlotWithGaps', () => {
    const css = stripBackground(['up', 'down']);
    expect(css).toBe(
      `linear-gradient(90deg, ${SLOT_COLOR.up} 0% calc(50% - 2px), transparent 0 50%, ${SLOT_COLOR.down} 50% 100%, transparent 0 100%)`,
    );
    expect(stripBackground([])).toBe('none');
  });

  it('Test_SlotAt_MapsPointerToSlot', () => {
    expect(slotAt(0, 480, 48)).toBe(0);
    expect(slotAt(9.9, 480, 48)).toBe(0);
    expect(slotAt(10, 480, 48)).toBe(1);
    expect(slotAt(479.9, 480, 48)).toBe(47);
    expect(slotAt(480, 480, 48)).toBeUndefined();
    expect(slotAt(-1, 480, 48)).toBeUndefined();
    expect(slotAt(5, 0, 48)).toBeUndefined();
  });

  it('Test_StripSummary_CountsByStatus', () => {
    expect(stripSummary(['up', 'up', 'down', 'none'])).toBe('2 Up, 1 Down, 1 no runs');
  });
});
