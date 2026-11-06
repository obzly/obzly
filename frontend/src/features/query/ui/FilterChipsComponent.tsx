import { Tag, Tooltip } from 'antd';
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
    <div className="flex min-w-0 flex-wrap items-center gap-1">
      {chips.map((clause) => {
        const { negated, field, value } = describeClause(clause);
        return (
          <Tag
            key={clause}
            closable
            onClose={(e) => {
              e.preventDefault();
              onRemoveClause(clause);
            }}
            color={negated ? 'red' : 'default'}
            className="!mr-0 max-w-[340px] !font-mono text-xs"
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
          </Tag>
        );
      })}
      {[...hiddenLevels].sort().map((level) => (
        <Tag
          key={`level:${level}`}
          closable
          onClose={(e) => {
            e.preventDefault();
            onShowLevel(level);
          }}
          color="red"
          className="!mr-0 !font-mono text-xs"
        >
          <span
            className="mr-1 inline-block h-2 w-2 rounded-full align-middle"
            style={{ background: levelColor(level) }}
          />
          level ≠ {level.toLowerCase()}
        </Tag>
      ))}
      {total > 1 && (
        <button
          type="button"
          onClick={onClearAll}
          className="ml-1 text-xs text-emerald-600 hover:underline"
        >
          Clear all
        </button>
      )}
    </div>
  );
};
