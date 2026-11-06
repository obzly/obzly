import {
  ClockCircleOutlined,
  CodeOutlined,
  DeleteOutlined,
  LoadingOutlined,
  PlayCircleOutlined,
  SearchOutlined,
  StarOutlined,
  StopOutlined,
  UndoOutlined,
} from '@ant-design/icons';
import { Button, Dropdown, Empty, Input, Modal, Segmented, Tooltip } from 'antd';
import React, { useEffect, useState } from 'react';

import type { QueryableField } from '../../../entity/query';
import { useTheme } from '../../../shared/theme';
import type { ValueSuggestion } from '../lib/queryEditor/searchLanguage';
import type { SavedSearch } from '../lib/savedQueriesStorage';
import { SearchQuerySyntaxError, parseSearchQuery } from '../lib/searchQueryParser';
import { QueryEditorComponent } from './QueryEditorComponent';

export type SearchMode = 'simple' | 'sql';

interface Props {
  mode: SearchMode;
  onModeChange: (mode: SearchMode) => void;
  simpleText: string;
  onSimpleTextChange: (text: string) => void;
  sqlText: string;
  onSqlTextChange: (text: string) => void;
  isExecuting: boolean;
  /** Starts a new search - also while one runs (it replaces it). */
  onRun: () => void;
  /** Stops the running search, keeping the rows found so far. */
  onStop?: () => void;
  /** Clears the search, filters, hidden levels and zoom. */
  onReset?: () => void;
  // Recent queries next to Run; omitted when unset (SQL mode has no history).
  recentQueries?: string[];
  onSelectRecentQuery?: (text: string) => void;
  /** Rendered between the input and Run - the time range / sort / Live controls. */
  controls?: React.ReactNode;
  savedSearches?: SavedSearch[];
  onSaveSearch?: (name: string) => void;
  onSelectSavedSearch?: (search: SavedSearch) => void;
  onDeleteSavedSearch?: (name: string) => void;
  /** Queryable fields - completion, hover help and unknown-field warnings. */
  fields?: QueryableField[];
  /** Top values of a field over the current range, for value completion. */
  loadFieldValues?: (field: string) => Promise<ValueSuggestion[]>;
}

const SIMPLE_PLACEHOLDER =
  'level:error AND message:"timeout" — or just type text to search messages';
const SQL_PLACEHOLDER =
  "SELECT level, message FROM logs WHERE level = 'error' ORDER BY timestamp DESC LIMIT 100";

/**
 * The log search bar: free text parsed client-side (searchQueryParser.ts), or
 * SQL mode. Each mode keeps its own text; switching doesn't translate.
 */
export const SearchBarComponent = ({
  mode,
  onModeChange,
  simpleText,
  onSimpleTextChange,
  sqlText,
  onSqlTextChange,
  isExecuting,
  onRun,
  onStop,
  onReset,
  recentQueries,
  onSelectRecentQuery,
  controls,
  savedSearches,
  onSaveSearch,
  onSelectSavedSearch,
  onDeleteSavedSearch,
  fields = [],
  loadFieldValues,
}: Props): React.JSX.Element => {
  const { theme } = useTheme();
  const [syntaxError, setSyntaxError] = useState<string | null>(null);
  const [saveName, setSaveName] = useState<string | null>(null); // non-null = save dialog open
  const currentText = mode === 'simple' ? simpleText : sqlText;

  useEffect(() => {
    if (mode !== 'simple') {
      setSyntaxError(null);
      return;
    }
    try {
      parseSearchQuery(simpleText);
      setSyntaxError(null);
    } catch (e: unknown) {
      setSyntaxError(e instanceof SearchQuerySyntaxError ? e.message : 'Invalid search syntax');
    }
  }, [mode, simpleText]);

  const submit = () => {
    if (mode === 'sql' || !syntaxError) onRun();
  };

  return (
    <div className="space-y-1">
      <div className="flex flex-wrap items-start gap-2">
        <Segmented
          value={mode}
          onChange={(value) => onModeChange(value as SearchMode)}
          options={[
            { label: 'Search', value: 'simple', icon: <SearchOutlined /> },
            { label: 'SQL', value: 'sql', icon: <CodeOutlined /> },
          ]}
        />

        <QueryEditorComponent
          mode={mode === 'simple' ? 'search' : 'sql'}
          value={currentText}
          onChange={mode === 'simple' ? onSimpleTextChange : onSqlTextChange}
          onSubmit={submit}
          placeholder={mode === 'simple' ? SIMPLE_PLACEHOLDER : SQL_PLACEHOLDER}
          fields={fields}
          loadValues={loadFieldValues}
          hasError={mode === 'simple' && !!syntaxError}
          isDark={theme === 'dark'}
        />

        {controls}

        {/* Never `loading`: that ignored clicks, so a slow search locked the page. */}
        <Tooltip title={mode === 'simple' ? 'Run search (Enter)' : 'Run SQL (Enter)'}>
          <Button
            type="primary"
            icon={isExecuting ? <LoadingOutlined spin /> : <PlayCircleOutlined />}
            onClick={onRun}
            disabled={mode === 'simple' && !!syntaxError}
            className="border-emerald-600 bg-emerald-600 hover:border-emerald-700 hover:bg-emerald-700"
          >
            Run
          </Button>
        </Tooltip>

        {isExecuting && onStop && (
          <Tooltip title="Stop the running search">
            <Button danger icon={<StopOutlined />} onClick={onStop}>
              Stop
            </Button>
          </Tooltip>
        )}

        {onReset && (
          <Tooltip title="Reset: clear the search, filters, hidden levels and zoom">
            <Button
              icon={<UndoOutlined />}
              onClick={onReset}
              aria-label="Reset search and filters"
            />
          </Tooltip>
        )}

        {mode === 'simple' && recentQueries && onSelectRecentQuery && (
          <Dropdown
            trigger={['click']}
            placement="bottomRight"
            menu={
              recentQueries.length > 0
                ? {
                    items: recentQueries.map((q, i) => ({
                      key: i,
                      label: <span className="font-mono text-xs">{q}</span>,
                      onClick: () => onSelectRecentQuery(q),
                    })),
                  }
                : undefined
            }
            popupRender={
              recentQueries.length === 0
                ? () => (
                    <div className="rounded-md border border-gray-200 bg-white p-3 shadow-lg dark:border-gray-700 dark:bg-gray-800">
                      <Empty
                        description="No recent queries yet"
                        image={Empty.PRESENTED_IMAGE_SIMPLE}
                      />
                    </div>
                  )
                : undefined
            }
          >
            <Tooltip title="Recent queries">
              <Button icon={<ClockCircleOutlined />} aria-label="Recent queries" />
            </Tooltip>
          </Dropdown>
        )}

        {savedSearches && onSaveSearch && onSelectSavedSearch && onDeleteSavedSearch && (
          <Dropdown
            trigger={['click']}
            placement="bottomRight"
            popupRender={() => (
              <div className="w-80 rounded-md border border-gray-200 bg-white p-1 shadow-lg dark:border-gray-700 dark:bg-gray-800">
                <button
                  type="button"
                  disabled={!currentText.trim()}
                  onClick={() => setSaveName('')}
                  className="w-full rounded px-2 py-1.5 text-left text-sm text-emerald-700 hover:bg-emerald-50 disabled:cursor-not-allowed disabled:text-gray-400 disabled:hover:bg-transparent dark:text-emerald-400 dark:hover:bg-emerald-900/30"
                >
                  Save current {mode === 'sql' ? 'SQL' : 'search'}…
                </button>
                <div className="my-1 border-t border-gray-100 dark:border-gray-700" />
                {savedSearches.length === 0 ? (
                  <div className="px-2 py-2 text-xs text-gray-400">No saved searches yet</div>
                ) : (
                  <ul className="max-h-72 overflow-y-auto">
                    {savedSearches.map((saved) => (
                      <li
                        key={saved.name}
                        className="group flex items-center gap-1 rounded hover:bg-gray-50 dark:hover:bg-gray-700"
                      >
                        <button
                          type="button"
                          onClick={() => onSelectSavedSearch(saved)}
                          className="min-w-0 flex-1 px-2 py-1 text-left"
                        >
                          <div className="truncate text-sm">
                            {saved.name}
                            {saved.mode === 'sql' && (
                              <span className="ml-1 text-[10px] text-gray-400">SQL</span>
                            )}
                          </div>
                          <div className="truncate font-mono text-[11px] text-gray-400">
                            {saved.text}
                          </div>
                        </button>
                        <Button
                          type="text"
                          size="small"
                          danger
                          icon={<DeleteOutlined />}
                          aria-label={`Delete saved search ${saved.name}`}
                          className="opacity-0 group-hover:opacity-100"
                          onClick={() => onDeleteSavedSearch(saved.name)}
                        />
                      </li>
                    ))}
                  </ul>
                )}
              </div>
            )}
          >
            <Tooltip title="Saved searches">
              <Button icon={<StarOutlined />} aria-label="Saved searches" />
            </Tooltip>
          </Dropdown>
        )}
      </div>

      <Modal
        title={`Save ${mode === 'sql' ? 'SQL query' : 'search'}`}
        open={saveName !== null}
        okText="Save"
        okButtonProps={{ disabled: !saveName?.trim() }}
        onOk={() => {
          if (saveName?.trim() && onSaveSearch) onSaveSearch(saveName.trim());
          setSaveName(null);
        }}
        onCancel={() => setSaveName(null)}
        destroyOnHidden
      >
        <Input
          autoFocus
          placeholder="Name, e.g. Checkout errors"
          value={saveName ?? ''}
          onChange={(e) => setSaveName(e.target.value)}
          onPressEnter={() => {
            if (saveName?.trim() && onSaveSearch) onSaveSearch(saveName.trim());
            setSaveName(null);
          }}
          maxLength={80}
        />
        <div className="mt-2 truncate font-mono text-xs text-gray-500">{currentText}</div>
        <div className="mt-1 text-xs text-gray-400">
          Saving with an existing name replaces it. Saved in this browser.
        </div>
      </Modal>

      {mode === 'simple' && syntaxError && (
        <div className="pl-1 text-xs text-red-500">{syntaxError}</div>
      )}
      {mode === 'sql' && (
        <div className="pl-1 text-xs text-gray-500 dark:text-gray-300">
          Queries a <code className="!font-mono">logs</code> table scoped to this project and time
          range — columns:{' '}
          <code className="!font-mono">
            timestamp, level, message, client_ip, trace_id, span_id, fields
          </code>{' '}
          (fields is JSON — use{' '}
          <code className="!font-mono">json_extract_string(fields, &apos;$.key&apos;)</code>).
        </div>
      )}
    </div>
  );
};
