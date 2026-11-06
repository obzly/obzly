import { describe, expect, it } from 'vitest';

import type { CheckView } from '../../../entity/synthetics';
import { dependencyLayout } from './dependencyLayout';

const check = (id: string, parentIds: string[] = []): CheckView =>
  ({ id, name: id, folder: '', parentIds }) as CheckView;

describe('dependencyLayout', () => {
  it('puts each monitor one layer below its deepest parent and leaves out unrelated ones', () => {
    const layout = dependencyLayout([
      check('dns'),
      check('gateway', ['dns']),
      check('checkout', ['gateway', 'dns']),
      check('blog'),
      check('ghost', ['deleted']),
    ]);
    const layer = Object.fromEntries(layout.nodes.map((node) => [node.check.id, node.layer]));
    expect(layer).toEqual({ dns: 0, gateway: 1, checkout: 2 });
    expect(layout.links).toHaveLength(3);
  });
});
