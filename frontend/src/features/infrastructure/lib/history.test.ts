import { describe, expect, it } from 'vitest';

import type { InventoryHistory } from '../../../entity/inventory';
import { mergeHistories } from './history';

const H1 = '2026-11-17T10:00:00Z';
const H2 = '2026-11-17T11:00:00Z';
const H3 = '2026-11-18T11:00:00Z';

describe('mergeHistories', () => {
  it("adds the Sites' hours they share and the counts of a day ago", () => {
    const eu: InventoryHistory = {
      hours: [H1, H2, H3],
      kinds: { pod: [4, 4, 6], node: [2, 2, 2] },
      previousAt: H2,
      previous: { pod: { healthy: 3, warning: 1 }, node: { healthy: 2 } },
    };
    const us: InventoryHistory = {
      hours: [H2, H3],
      kinds: { pod: [1, 2] },
      previousAt: H2,
      previous: { pod: { healthy: 1 } },
    };
    expect(mergeHistories([eu, us])).toEqual({
      trend: { pod: [5, 8], node: [2, 2] },
      previous: { pod: 5, node: 2 },
    });
  });

  it('leaves the pills out when a Site has nothing a day old or no history', () => {
    const fresh: InventoryHistory = { hours: [H3], kinds: { pod: [2] } };
    const old: InventoryHistory = {
      hours: [H2, H3],
      kinds: { pod: [1, 1] },
      previousAt: H2,
      previous: { pod: { healthy: 1 } },
    };
    expect(mergeHistories([old, fresh]).previous).toBeUndefined();
    expect(mergeHistories([old, undefined])).toEqual({ trend: {} });
    expect(mergeHistories([])).toEqual({ trend: {} });
  });
});
