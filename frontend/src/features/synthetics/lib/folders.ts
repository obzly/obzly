import type { CheckView } from '../../../entity/synthetics';
import type { MonitorStatus } from './status';

export interface FolderRow {
  kind: 'folder';
  key: string;
  /** Full path, e.g. "Payments/EU". */
  path: string;
  name: string;
  counts: Partial<Record<MonitorStatus, number>>;
  total: number;
  children: TreeRow[];
}

export interface CheckRow {
  kind: 'check';
  key: string;
  check: CheckView;
}

export type TreeRow = FolderRow | CheckRow;

/** Every folder path and its ancestors ("Payments/EU" also gives "Payments"), sorted. */
export const folderPaths = (checks: CheckView[]): string[] => {
  const paths = new Set<string>();
  for (const check of checks) {
    const parts = (check.folder ?? '').split('/').filter(Boolean);
    parts.forEach((_, index) => paths.add(parts.slice(0, index + 1).join('/')));
  }
  return [...paths].sort((a, b) => a.localeCompare(b));
};

/** Whether check sits in folder or below it. */
export const inFolder = (check: CheckView, folder: string): boolean =>
  check.folder === folder || (check.folder ?? '').startsWith(`${folder}/`);

/**
 * Monitors grouped by folder, folders first (by name) then monitors in the
 * given order, at every level. Each folder counts the statuses below it.
 */
export const folderTree = (
  checks: CheckView[],
  compare: (a: CheckView, b: CheckView) => number,
  statusOf: (check: CheckView) => MonitorStatus,
): TreeRow[] => {
  const root: FolderRow = folder('', '');
  const folders = new Map<string, FolderRow>([['', root]]);
  const folderFor = (path: string): FolderRow => {
    const existing = folders.get(path);
    if (existing) return existing;
    const slash = path.lastIndexOf('/');
    const parent = folderFor(slash < 0 ? '' : path.slice(0, slash));
    const created = folder(path, path.slice(slash + 1));
    parent.children.push(created);
    folders.set(path, created);
    return created;
  };
  for (const check of [...checks].sort(compare)) {
    const path = (check.folder ?? '').split('/').filter(Boolean).join('/');
    const status = statusOf(check);
    folderFor(path).children.push({ kind: 'check', key: check.id, check });
    for (let at: string | undefined = path; at !== undefined; at = parentPath(at)) {
      const counted = folders.get(at)!;
      counted.counts[status] = (counted.counts[status] ?? 0) + 1;
      counted.total += 1;
    }
  }
  const order = (rows: TreeRow[]) => {
    rows.sort((a, b) =>
      a.kind === b.kind
        ? a.kind === 'folder'
          ? a.name.localeCompare((b as FolderRow).name)
          : 0
        : a.kind === 'folder'
          ? -1
          : 1,
    );
    rows.forEach((row) => row.kind === 'folder' && order(row.children));
  };
  order(root.children);
  return root.children;
};

const folder = (path: string, name: string): FolderRow => ({
  kind: 'folder',
  key: `folder:${path}`,
  path,
  name,
  counts: {},
  total: 0,
  children: [],
});

const parentPath = (path: string): string | undefined => {
  if (path === '') return undefined;
  const slash = path.lastIndexOf('/');
  return slash < 0 ? '' : path.slice(0, slash);
};
