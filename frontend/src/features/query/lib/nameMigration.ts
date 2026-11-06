import type { QueryNode } from '../../../entity/query';
import { normalizeAttributeName } from '../../../shared/lib/attributeNames';

// One-time rewrite of queries saved in this browser to the underscore
// naming rule (k8s.pod.name -> k8s_pod_name). Old names still work on the
// server; this keeps what users see consistent with the field list.
const DONE_KEY = 'obzly-attribute-names-v2';
const FIELD_BEFORE_OPERATOR = /(^|[\s(-])([a-zA-Z_][a-zA-Z0-9_.]*)(?=!=|>=|<=|:|=|>|<)/g;

/** Renames field names in search-bar text, leaving quoted phrases alone. */
export function migrateSearchText(text: string): string {
  return text
    .split(/("(?:[^"\\]|\\.)*")/)
    .map((part, index) =>
      index % 2 === 1
        ? part
        : part.replace(
            FIELD_BEFORE_OPERATOR,
            (_all, before: string, field: string) => before + normalizeAttributeName(field),
          ),
    )
    .join('');
}

/** Rewrites '$."resource.k8s.pod.name"' JSON paths in SQL to '$.k8s_pod_name'. */
export function migrateSqlText(sql: string): string {
  return sql.replace(
    /'\$\."([^"]+)"'/g,
    (_all, key: string) => `'$.${normalizeAttributeName(key)}'`,
  );
}

function migrateQueryNode(node: QueryNode | null): QueryNode | null {
  if (!node) return node;
  if (node.condition) {
    node.condition.field = normalizeAttributeName(node.condition.field);
  }
  node.logic?.children.forEach((child) => migrateQueryNode(child));
  return node;
}

function rewrite(key: string, fn: (value: unknown) => unknown): void {
  const raw = localStorage.getItem(key);
  if (!raw) return;
  localStorage.setItem(key, JSON.stringify(fn(JSON.parse(raw))));
}

export function migrateStoredQueryNames(): void {
  try {
    if (localStorage.getItem(DONE_KEY)) return;

    const keys: string[] = [];
    for (let i = 0; i < localStorage.length; i++) {
      const key = localStorage.key(i);
      if (key) keys.push(key);
    }

    for (const key of keys) {
      try {
        if (key.startsWith('obzly-recent-queries:')) {
          rewrite(key, (value) => (value as string[]).map(migrateSearchText));
        } else if (key.startsWith('obzly-saved-searches:')) {
          rewrite(key, (value) =>
            (value as { name: string; mode: string; text: string }[]).map((saved) => ({
              ...saved,
              text:
                saved.mode === 'sql' ? migrateSqlText(saved.text) : migrateSearchText(saved.text),
            })),
          );
        } else if (key.startsWith('obzly-frequent-fields:')) {
          rewrite(key, (value) => {
            const counts: Record<string, number> = {};
            for (const [field, count] of Object.entries(value as Record<string, number>)) {
              const name = normalizeAttributeName(field);
              counts[name] = (counts[name] ?? 0) + count;
            }
            return counts;
          });
        } else if (key.startsWith('obzly-query-results-columns')) {
          rewrite(key, (value) => [
            ...new Set(
              (value as string[]).map((id) =>
                id.startsWith('field:') ? `field:${normalizeAttributeName(id.slice(6))}` : id,
              ),
            ),
          ]);
        } else if (/^obzly-query-[0-9a-f]{8}-[0-9a-f]{4}-/.test(key)) {
          rewrite(key, (value) => {
            const saved = value as {
              simpleText?: string;
              sqlText?: string;
              query?: QueryNode | null;
            };
            return {
              ...saved,
              simpleText: saved.simpleText ? migrateSearchText(saved.simpleText) : saved.simpleText,
              sqlText: saved.sqlText ? migrateSqlText(saved.sqlText) : saved.sqlText,
              query: migrateQueryNode(saved.query ?? null),
            };
          });
        }
      } catch {
        // One unreadable entry never blocks the rest.
      }
    }
    localStorage.setItem(DONE_KEY, '1');
  } catch {
    // Storage unavailable (private mode) - nothing to migrate.
  }
}
