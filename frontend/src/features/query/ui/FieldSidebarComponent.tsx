import {
  SearchOutlined,
  SortAscendingOutlined,
  SortDescendingOutlined,
  StarOutlined,
} from '@ant-design/icons';
import { Checkbox, Collapse, Input, Pagination, Spin, Tooltip } from 'antd';
import dayjs from 'dayjs';
import React, { useEffect, useMemo, useRef, useState } from 'react';

import { type QueryNode, type QueryableField, queryApi } from '../../../entity/query';
import { getFrequentFields, recordFieldUsage } from '../lib/frequentFieldsStorage';
import { levelColor, sortLevels, valueColor } from '../lib/logLevels';
import type { TimeRange } from './TimeRangePickerComponent';

interface Props {
  projectId: string;
  fields: QueryableField[];
  /** The query that actually runs (search + severity facet) - scopes top values. */
  query: QueryNode | null;
  /** Per-level counts without the severity facet's filter, so hidden levels still show theirs. */
  levelCounts: Record<string, number>;
  isLevelCountsLoading: boolean;
  timeRange: TimeRange | null;
  /** Bumped on every run - refreshes counts and any expanded facet. */
  refreshToken: number;
  hiddenLevels: ReadonlySet<string>;
  onToggleLevel: (level: string, visible: boolean) => void;
  /** Hide every listed level but this one (`listedLevels` = what the facet shows). */
  onShowOnlyLevel: (level: string, listedLevels: string[]) => void;
  onAddFilter: (fieldName: string, value: string) => void;
  /** A recent value (not logged in the range): widen the range, then filter. */
  onAddRecentFilter: (fieldName: string, value: string, lastSeen: string) => void;
}

interface FacetValue {
  key: string;
  count: number;
  /** Display text when it differs from the filter value (pattern_hash). */
  label?: string;
  /** Hover text - a real sample message for a pattern. */
  title?: string;
  /** Set on a recent value: no count, offered from the log source index. */
  lastSeen?: string;
  /** A recent value that logged inside the range (outside the sample). */
  inRange?: boolean;
}

interface FieldTopValues {
  loading: boolean;
  error: string | null;
  values: FacetValue[];
  /** Set when the values were counted from this time to the range's end only. */
  sampledFrom?: string;
  /** Recent values weren't looked up: the search filters on other fields. */
  recentSkipped?: boolean;
}

const PATTERN_FIELD = 'pattern_hash';
// Facets list every value the backend serves (patterns cap at 200); hitting
// the cap is shown in the footer rather than silently truncating.
const FACET_VALUES_LIMIT = 200;
const TOP_PATTERNS_LIMIT = 200;
// Facets count only the newest matches up to this many, so a long range over
// a busy project doesn't scan every file.
const FACET_SAMPLE_ROWS = 100_000;
// Paged so a long facet doesn't push the rest off-screen.
const FACET_PAGE_SIZE = 10;
const FREQUENT_FIELDS_LIMIT = 6;
// Clicking a value to filter is a stronger "I use this field" signal than
// expanding it.
const EXPAND_WEIGHT = 1;
const ADD_FILTER_WEIGHT = 3;
// level has its own severity facet; timestamp values are near-unique instants
// and filter by the time picker or histogram instead.
const FIELDS_HIDDEN_FROM_LIST = new Set(['level', 'timestamp']);

const compactCount = (n: number): string =>
  n === 0 ? '-' : Intl.NumberFormat('en', { notation: 'compact' }).format(n);

/**
 * The logs page's attribute sidebar: a severity facet whose counts ignore its
 * own filter, and lazily fetched, click-to-filter value facets per field.
 */
export const FieldSidebarComponent = ({
  projectId,
  fields,
  query,
  levelCounts,
  isLevelCountsLoading,
  timeRange,
  refreshToken,
  hiddenLevels,
  onToggleLevel,
  onShowOnlyLevel,
  onAddFilter,
  onAddRecentFilter,
}: Props): React.JSX.Element => {
  const [valuesByField, setValuesByField] = useState<Record<string, FieldTopValues>>({});
  const [openKeys, setOpenKeys] = useState<string[]>([]);
  const [search, setSearch] = useState('');
  const [sortDescending, setSortDescending] = useState(false);

  // Read every render (cheap) so "Frequently Used" updates right after a click.
  const frequentFieldNames = getFrequentFields(projectId, FREQUENT_FIELDS_LIMIT);

  const needle = search.trim().toLowerCase();
  const matchesSearch = (f: QueryableField) => !needle || f.name.toLowerCase().includes(needle);

  const realFields = useMemo(() => {
    const list = fields.filter(
      (f) => !f.name.includes('*') && !FIELDS_HIDDEN_FROM_LIST.has(f.name),
    );
    list.sort((a, b) =>
      sortDescending ? b.name.localeCompare(a.name) : a.name.localeCompare(b.name),
    );
    return list;
  }, [fields, sortDescending]);

  const fieldsByName = new Map(realFields.map((f) => [f.name, f]));
  const frequentFields = frequentFieldNames
    .map((name) => fieldsByName.get(name))
    .filter((f): f is QueryableField => f !== undefined && matchesSearch(f));
  const visibleFields = realFields.filter(matchesSearch);

  const handleAddFilter = (fieldName: string, value: string) => {
    recordFieldUsage(projectId, fieldName, ADD_FILTER_WEIGHT);
    onAddFilter(fieldName, value);
  };

  const handleAddRecentFilter = (fieldName: string, value: string, lastSeen: string) => {
    recordFieldUsage(projectId, fieldName, ADD_FILTER_WEIGHT);
    onAddRecentFilter(fieldName, value, lastSeen);
  };

  // Hidden levels stay listed so they can be re-checked; empty levels are left out.
  const levels = sortLevels([
    ...Object.keys(levelCounts).filter((level) => (levelCounts[level] ?? 0) > 0),
    ...hiddenLevels,
  ]);
  const [isSeverityOpen, setIsSeverityOpen] = useState(true);
  const severityMatchesSearch = !needle || 'level'.includes(needle) || 'severity'.includes(needle);

  // Per-field request sequence, so a slow response for an old query can't
  // overwrite a newer one.
  const requestSeqRef = useRef<Record<string, number>>({});

  const loadTopValues = (fieldName: string) => {
    if (!timeRange) return;
    const seq = (requestSeqRef.current[fieldName] ?? 0) + 1;
    requestSeqRef.current[fieldName] = seq;
    const isCurrent = () => requestSeqRef.current[fieldName] === seq;

    setValuesByField((prev) => ({
      ...prev,
      [fieldName]: { loading: true, error: null, values: prev[fieldName]?.values ?? [] },
    }));

    const range = { from: timeRange.from.toISOString(), to: timeRange.to.toISOString() };
    const request: Promise<{
      values: FacetValue[];
      sampledFrom?: string;
      recentSkipped?: boolean;
    }> =
      fieldName === PATTERN_FIELD
        ? queryApi
            .getPatterns(projectId, {
              query,
              timeRange: range,
              limit: TOP_PATTERNS_LIMIT,
              sampleRows: FACET_SAMPLE_ROWS,
            })
            .then((response) => ({
              sampledFrom: response.sampledFrom,
              values: response.patterns.map((p) => ({
                key: p.hash,
                count: p.count,
                label: p.pattern || '(empty message)',
                title: p.sample,
              })),
            }))
        : queryApi
            .getFieldValues(projectId, {
              field: fieldName,
              query,
              timeRange: range,
              limit: FACET_VALUES_LIMIT,
              sampleRows: FACET_SAMPLE_ROWS,
              includeRecent: true,
            })
            .then((response) => ({
              sampledFrom: response.sampledFrom,
              recentSkipped: response.recentSkipped,
              values: [
                ...response.values.map((v) => ({ key: v.value, count: v.count })),
                ...(response.recent ?? []).map((v) => ({
                  key: v.value,
                  count: 0,
                  lastSeen: v.lastSeen,
                  inRange: v.inRange,
                })),
              ],
            }));

    request
      .then(({ values, sampledFrom, recentSkipped }) => {
        if (!isCurrent()) return;
        setValuesByField((prev) => ({
          ...prev,
          [fieldName]: { loading: false, error: null, values, sampledFrom, recentSkipped },
        }));
      })
      .catch((e: unknown) => {
        if (!isCurrent()) return;
        setValuesByField((prev) => ({
          ...prev,
          [fieldName]: { loading: false, error: (e as Error).message, values: [] },
        }));
      });
  };

  // A new query/range/run makes cached facets stale: refetch only the open ones.
  const openKeysRef = useRef<string[]>([]);
  useEffect(() => {
    openKeysRef.current = openKeys;
  }, [openKeys]);
  useEffect(() => {
    setValuesByField({});
    for (const key of openKeysRef.current) loadTopValues(key);
  }, [projectId, query, timeRange, refreshToken]);

  // Both Collapses share one open set; `listKeys` limits a close to the
  // reporting list, so it never collapses a facet only the other list shows.
  const onExpand = (keys: string | string[], listKeys: ReadonlySet<string>) => {
    const active = Array.isArray(keys) ? keys : [keys];
    const next = openKeys.filter((key) => active.includes(key) || !listKeys.has(key));
    for (const key of active) {
      if (openKeys.includes(key)) continue;
      recordFieldUsage(projectId, key, EXPAND_WEIGHT);
      if (!valuesByField[key] || valuesByField[key].error) loadTopValues(key);
      next.push(key);
    }
    setOpenKeys(next);
  };

  const toCollapseItem = (field: QueryableField) => ({
    key: field.name,
    label: <span className="font-mono text-xs">{field.name}</span>,
    children: (
      <FieldTopValuesList
        state={valuesByField[field.name]}
        isPattern={field.name === PATTERN_FIELD}
        onAddFilter={(v) => handleAddFilter(field.name, v)}
        onAddRecentFilter={(v, lastSeen) => handleAddRecentFilter(field.name, v, lastSeen)}
      />
    ),
  });

  const frequentKeys = new Set(frequentFields.map((f) => f.name));
  const allKeys = new Set(visibleFields.map((f) => f.name));

  return (
    <div className="w-64 shrink-0 overflow-y-auto rounded-lg border border-gray-200 bg-white p-2 shadow-sm dark:border-gray-700 dark:bg-gray-800">
      <div className="flex items-center gap-1 px-1 pb-2">
        <Input
          size="small"
          allowClear
          prefix={<SearchOutlined className="text-gray-400" />}
          placeholder="Search attributes"
          value={search}
          onChange={(e) => setSearch(e.target.value)}
        />
        <Tooltip title={sortDescending ? 'Sorted Z-A' : 'Sorted A-Z'}>
          <button
            type="button"
            onClick={() => setSortDescending((d) => !d)}
            className="shrink-0 rounded p-1 text-gray-500 hover:bg-gray-100 dark:text-gray-300 dark:hover:bg-gray-700"
            aria-label="Toggle attribute sort order"
          >
            {sortDescending ? <SortDescendingOutlined /> : <SortAscendingOutlined />}
          </button>
        </Tooltip>
      </div>

      {frequentFields.length > 0 && (
        <>
          <div className="flex items-center gap-1 px-2 py-1 text-xs font-medium text-gray-500 uppercase dark:text-gray-300">
            <StarOutlined className="text-[10px]" />
            Frequently Used
          </div>
          <Collapse
            ghost
            size="small"
            activeKey={openKeys.filter((k) => frequentKeys.has(k))}
            onChange={(keys) => onExpand(keys, frequentKeys)}
            items={frequentFields.map(toCollapseItem)}
          />
          <div className="my-1 border-t border-gray-100 dark:border-gray-700" />
        </>
      )}

      {severityMatchesSearch && (
        <>
          <Collapse
            ghost
            size="small"
            activeKey={isSeverityOpen ? ['level'] : []}
            onChange={(keys) => setIsSeverityOpen((Array.isArray(keys) ? keys : [keys]).length > 0)}
            items={[
              {
                key: 'level',
                label: (
                  <span className="flex items-center gap-2 font-mono text-xs">
                    level
                    {isLevelCountsLoading && <Spin size="small" />}
                  </span>
                ),
                children:
                  levels.length === 0 ? (
                    <div className="text-xs text-gray-400 dark:text-gray-400">
                      {isLevelCountsLoading ? '' : 'No values in range'}
                    </div>
                  ) : (
                    <ul className="space-y-0.5">
                      {levels.map((level) => {
                        const visible = !hiddenLevels.has(level);
                        return (
                          <li
                            key={level}
                            className="group flex items-center gap-2 rounded px-1 py-0.5 hover:bg-gray-50 dark:hover:bg-gray-700"
                          >
                            <Checkbox
                              checked={visible}
                              onChange={(e) => onToggleLevel(level, e.target.checked)}
                            >
                              <span className="flex items-center gap-2 text-xs">
                                <ColorDot color={levelColor(level)} />
                                {level.toLowerCase()}
                              </span>
                            </Checkbox>
                            <button
                              type="button"
                              onClick={() => onShowOnlyLevel(level, levels)}
                              className="ml-auto hidden text-[10px] text-emerald-600 group-hover:inline"
                              title={`Show only ${level}`}
                            >
                              only
                            </button>
                            <span className="ml-auto w-10 text-right text-xs text-gray-400 group-hover:hidden dark:text-gray-400">
                              {compactCount(levelCounts[level] ?? 0)}
                            </span>
                          </li>
                        );
                      })}
                    </ul>
                  ),
              },
            ]}
          />
          <div className="my-1 border-t border-gray-100 dark:border-gray-700" />
        </>
      )}

      <div className="px-2 py-1 text-xs font-medium text-gray-500 uppercase dark:text-gray-300">
        Attributes
      </div>
      {visibleFields.length === 0 ? (
        <div className="px-2 py-1 text-xs text-gray-400">No attributes match “{search}”</div>
      ) : (
        <Collapse
          ghost
          size="small"
          activeKey={openKeys.filter((k) => allKeys.has(k))}
          onChange={(keys) => onExpand(keys, allKeys)}
          items={visibleFields.map(toCollapseItem)}
        />
      )}
    </div>
  );
};

const FieldTopValuesList = ({
  state,
  isPattern,
  onAddFilter,
  onAddRecentFilter,
}: {
  state?: FieldTopValues;
  isPattern: boolean;
  onAddFilter: (value: string) => void;
  onAddRecentFilter: (value: string, lastSeen: string) => void;
}): React.JSX.Element => {
  const [page, setPage] = useState(1);

  // A refetch replaces the values, so go back to page 1.
  const values = state?.values;
  useEffect(() => {
    setPage(1);
  }, [values]);

  if (!state || (state.loading && state.values.length === 0)) {
    return (
      <div className="flex justify-center py-2">
        <Spin size="small" />
      </div>
    );
  }
  if (state.error) {
    return <div className="text-xs text-red-500">{state.error}</div>;
  }
  const recentSkippedHint = state.recentSkipped && (
    <div className="mt-1 px-1 text-[10px] text-gray-400 dark:text-gray-500">
      Values seen in the last 36 h are hidden: the search filters on other fields
    </div>
  );
  if (state.values.length === 0) {
    return (
      <>
        <div className="text-xs text-gray-400 dark:text-gray-400">No values in range</div>
        {recentSkippedHint}
      </>
    );
  }

  const total = state.values.length;
  // Counted values come first; recent ones (lastSeen set) follow them.
  const countedTotal = state.values.filter((v) => !v.lastSeen).length;
  const isPaged = total > FACET_PAGE_SIZE;
  const start = isPaged ? (page - 1) * FACET_PAGE_SIZE : 0;
  const pageValues = isPaged ? state.values.slice(start, start + FACET_PAGE_SIZE) : state.values;
  const limit = isPattern ? TOP_PATTERNS_LIMIT : FACET_VALUES_LIMIT;

  return (
    <div className={state.loading ? 'opacity-60' : ''}>
      {countedTotal === 0 && (
        <div className="mb-1 text-xs text-gray-400 dark:text-gray-400">No values in range</div>
      )}
      <ul className="space-y-1">
        {pageValues.map((v, index) =>
          v.lastSeen ? (
            <RecentValueRow
              key={v.key}
              value={v}
              // The heading opens the recent list, and repeats atop each page of it.
              showHeading={index === 0 || !pageValues[index - 1].lastSeen}
              onAddFilter={onAddRecentFilter}
            />
          ) : (
            <li key={v.key}>
              <Tooltip
                title={v.title ? `e.g. ${v.title}` : undefined}
                placement="right"
                mouseEnterDelay={0.4}
              >
                <button
                  type="button"
                  onClick={() => onAddFilter(v.key)}
                  className="flex w-full items-center justify-between gap-2 rounded px-1 py-0.5 text-left text-xs text-gray-800 hover:bg-emerald-50 dark:text-gray-100 dark:hover:bg-emerald-900/40"
                  aria-label={`Add filter: ${v.key}`}
                >
                  <span className="flex min-w-0 items-center gap-2">
                    {/* Absolute index, so a value keeps its color across pages. */}
                    <ColorDot color={valueColor(start + index)} />
                    <span className={`truncate ${isPattern ? 'font-mono text-[11px]' : ''}`}>
                      {v.label ?? v.key}
                    </span>
                  </span>
                  <span className="shrink-0 text-gray-400 dark:text-gray-400">
                    {v.count.toLocaleString()}
                  </span>
                </button>
              </Tooltip>
            </li>
          ),
        )}
      </ul>

      {isPaged && (
        <div className="mt-1 flex justify-center">
          <Pagination
            simple
            size="small"
            current={page}
            total={total}
            pageSize={FACET_PAGE_SIZE}
            onChange={setPage}
          />
        </div>
      )}

      {state.sampledFrom && (
        <div className="mt-1 px-1 text-[10px] text-gray-400 dark:text-gray-500">
          Newest {FACET_SAMPLE_ROWS.toLocaleString()} matching logs, since{' '}
          {dayjs(state.sampledFrom).format('MMM D, HH:mm')}
        </div>
      )}

      {recentSkippedHint}

      {countedTotal >= limit && (
        <div className="mt-1 px-1 text-[10px] text-gray-400 dark:text-gray-500">
          Top {limit} values - narrow the search to see more
        </div>
      )}
    </div>
  );
};

const RecentValueRow = ({
  value,
  showHeading,
  onAddFilter,
}: {
  value: FacetValue;
  showHeading: boolean;
  onAddFilter: (value: string, lastSeen: string) => void;
}): React.JSX.Element => {
  const lastSeen = value.lastSeen ?? '';
  const when = value.inRange ? 'in range' : dayjs(lastSeen).fromNow();
  return (
    <li>
      {showHeading && (
        <div className="mt-2 mb-1 border-t border-gray-100 px-1 pt-1 text-[10px] font-medium text-gray-400 uppercase dark:border-gray-700 dark:text-gray-500">
          Seen in the last 36 h
        </div>
      )}
      <Tooltip
        title={
          value.inRange
            ? 'Logged in this range, outside the newest logs counted'
            : `Last seen ${dayjs(lastSeen).format('MMM D, HH:mm')}; filtering widens the range to include it`
        }
        placement="right"
        mouseEnterDelay={0.4}
      >
        <button
          type="button"
          onClick={() => onAddFilter(value.key, lastSeen)}
          className="flex w-full items-center justify-between gap-2 rounded px-1 py-0.5 text-left text-xs text-gray-500 hover:bg-emerald-50 dark:text-gray-400 dark:hover:bg-emerald-900/40"
          aria-label={`Add filter: ${value.key} (last seen ${when})`}
        >
          <span className="flex min-w-0 items-center gap-2">
            <ColorDot color="#9ca3af" />
            <span className="truncate">{value.key}</span>
          </span>
          <span className="shrink-0 text-[10px] text-gray-400 dark:text-gray-500">{when}</span>
        </button>
      </Tooltip>
    </li>
  );
};

const ColorDot = ({ color }: { color: string }): React.JSX.Element => (
  <span
    aria-hidden
    className="inline-block h-2 w-2 shrink-0 rounded-full"
    style={{ background: color }}
  />
);
