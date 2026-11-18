import { CloseOutlined } from '@ant-design/icons';
import { Tooltip } from 'antd';
import React from 'react';

import { levelColor } from '../lib/logLevels';

interface Props {
  /** The search's top-level AND clauses (splitTopLevelAndClauses), or null when not that flat shape. */
  clauses: string[] | null;
  /** Levels unchecked in the severity facet - shown as chips too. */
  hiddenLevels: ReadonlySet<string>;
  onRemoveClause: (clause: string) => void;
  onShowLevel: (level: string) => void;
  onClearAll: () => void;
}

// "field:value" / "-field:value" / free text -> what the chip shows.
function describeClause(clause: string): { negated: boolean; field: string | null; value: string } {
  const negated = clause.startsWith('-') && clause.length > 1;
  const body = negated ? clause.slice(1) : clause;
  const colon = body.startsWith('"') ? -1 : body.indexOf(':');
  if (colon <= 0) return { negated, field: null, value: body };
  let value = body.slice(colon + 1);
  if (value.startsWith('"') && value.endsWith('"') && value.length >= 2) {
    value = value.slice(1, -1).replace(/\\"/g, '"').replace(/\\\\/g, '\\');
  }
  return { negated, field: body.slice(0, colon), value };
}

/**
 * Active filters as removable chips: one per top-level AND clause plus hidden
 * levels. Non-flat searches show none, since removing a clause by text could
 * change the query's logic.
 */
export const FilterChipsComponent = ({
  clauses,
  hiddenLevels,
  onRemoveClause,
  onShowLevel,
  onClearAll,
}: Props): React.JSX.Element | null => {
  const chips = clauses ?? [];
  const total = chips.length + hiddenLevels.size;
  if (total === 0) return null;

  return (
    <div className="flex min-w-0 flex-wrap items-center gap-1.5">
      <span className="text-xs text-gray-500 dark:text-gray-400">Filters</span>
      {chips.map((clause) => {
        const { negated, field, value } = describeClause(clause);
        return (
          <Chip
            key={clause}
            negated={negated}
            removeLabel={`Remove filter ${clause}`}
            onRemove={() => onRemoveClause(clause)}
          >
            <Tooltip title={clause} mouseEnterDelay={0.5}>
              <span className="inline-block max-w-[300px] truncate align-bottom">
                {field ? (
                  <>
                    <span className="text-gray-500 dark:text-gray-400">{field}</span>
                    <span className="mx-1 font-bold">{negated ? '≠' : '='}</span>
                    {value}
                  </>
                ) : (
                  <>
                    {negated && <span className="mr-1 font-bold">NOT</span>}
                    {value}
                  </>
                )}
              </span>
            </Tooltip>
          </Chip>
        );
      })}
      {[...hiddenLevels].sort().map((level) => (
        <Chip
          key={`level:${level}`}
          negated
          removeLabel={`Show ${level.toLowerCase()} logs again`}
          onRemove={() => onShowLevel(level)}
        >
          <span
            className="mr-1 inline-block h-2 w-2 rounded-full align-middle"
            style={{ background: levelColor(level) }}
          />
          level ≠ {level.toLowerCase()}
        </Chip>
      ))}
      {total > 1 && (
        <button
          type="button"
          onClick={onClearAll}
          className="text-accent-600 dark:text-accent-400 ml-1 cursor-pointer text-xs hover:underline"
        >
          Clear all
        </button>
      )}
    </div>
  );
};

/** A removable v2 filter chip; red-tinted when it excludes. */
const Chip = ({
  negated,
  removeLabel,
  onRemove,
  children,
}: {
  negated: boolean;
  removeLabel: string;
  onRemove: () => void;
  children: React.ReactNode;
}): React.JSX.Element => (
  <span
    className={`inline-flex max-w-[340px] items-center gap-1 rounded-lg border py-0.5 pr-1 pl-2 font-mono text-xs ${
      negated
        ? 'border-red-200 bg-red-50 text-red-700 dark:border-red-900/60 dark:bg-red-950/40 dark:text-red-300'
        : 'border-gray-200 bg-gray-50 text-gray-800 dark:border-gray-700 dark:bg-gray-900/60 dark:text-gray-100'
    }`}
  >
    {children}
    <button
      type="button"
      onClick={onRemove}
      aria-label={removeLabel}
      className="flex h-4 w-4 shrink-0 cursor-pointer items-center justify-center rounded text-[10px] text-gray-400 hover:bg-gray-200 hover:text-gray-700 dark:hover:bg-gray-700 dark:hover:text-gray-100"
    >
      <CloseOutlined />
    </button>
  </span>
);
