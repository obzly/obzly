import { describe, expect, it } from 'vitest';

import { heatmapTooltip } from './heatmapTooltip';

describe('heatmapTooltip', () => {
  it('Test_HeatmapTooltip_EscapesLocationLabel', () => {
    const format = heatmapTooltip(['<img src=x onerror=alert(1)>'], [Date.UTC(2026, 0, 1)]);
    const html = format({ value: [0, 0, 97.5] });
    expect(html).not.toContain('<img');
    expect(html).toContain('&lt;img src=x onerror=alert(1)&gt;');
    expect(html).toContain('97.5% of runs passed');
  });
});
