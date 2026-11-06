import { describe, expect, it } from 'vitest';

import type { CheckView } from '../../../entity/synthetics';
import { type FolderRow, folderPaths, folderTree, inFolder } from './folders';

const check = (id: string, folder = ''): CheckView => ({ id, name: id, folder }) as CheckView;

describe('folderTree', () => {
  it('nests paths, puts folders first and counts statuses up the tree', () => {
    const checks = [
      check('b', 'Payments/EU'),
      check('a'),
      check('c', 'Payments'),
      check('d', 'Payments/EU'),
    ];
    const status = (item: CheckView) => (item.id === 'd' ? 'down' : 'up');
    const tree = folderTree(checks, (x, y) => x.name.localeCompare(y.name), status);

    expect(tree.map((row) => row.key)).toEqual(['folder:Payments', 'a']);
    const payments = tree[0] as FolderRow;
    expect(payments.total).toBe(3);
    expect(payments.counts).toEqual({ up: 2, down: 1 });
    expect(payments.children.map((row) => row.key)).toEqual(['folder:Payments/EU', 'c']);
    const eu = payments.children[0] as FolderRow;
    expect(eu.name).toBe('EU');
    expect(eu.children.map((row) => row.key)).toEqual(['b', 'd']);
  });
});

describe('folderPaths and inFolder', () => {
  it('lists ancestors and matches a subtree, not a name prefix', () => {
    const checks = [check('a', 'Payments/EU'), check('b', 'Platform')];
    expect(folderPaths(checks)).toEqual(['Payments', 'Payments/EU', 'Platform']);
    expect(inFolder(checks[0], 'Payments')).toBe(true);
    expect(inFolder(check('x', 'PaymentsOld'), 'Payments')).toBe(false);
  });
});
