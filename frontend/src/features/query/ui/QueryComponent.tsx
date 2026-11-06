import {
  DownOutlined,
  PauseCircleOutlined,
  PlayCircleOutlined,
  RightOutlined,
  SortAscendingOutlined,
  SortDescendingOutlined,
} from '@ant-design/icons';
import { App, Button, Tooltip } from 'antd';
import dayjs from 'dayjs';
import React, { useEffect, useMemo, useRef, useState } from 'react';

import type { Project } from '../../../entity/projects';
import { projectApi } from '../../../entity/projects';
import {
  type GetQueryableFieldsRequest,
  type LogContextScope,
  type LogItem,
  type LogQueryRequest,
  type LogQueryResponse,
  type QueryNode,
  type QueryableField,
  type SQLQueryResponse,
  SearchStreamUnavailableError,
  queryApi,
} from '../../../entity/query';
import type { UserProfile } from '../../../entity/users/model/UserProfile';
import { withHiddenLevels } from '../lib/logLevels';
import { type PushLiveTailHandle, startPushLiveTail } from '../lib/pushLiveTail';
import { type ValueSuggestion, clearValueSuggestions } from '../lib/queryEditor/searchLanguage';
import { getRecentQueries, recordRecentQuery } from '../lib/recentQueriesStorage';
import {
  type SavedSearch,
  deleteSavedSearch,
  getSavedSearches,
  saveSearch,
} from '../lib/savedQueriesStorage';
import {
  SearchQuerySyntaxError,
  parseSearchQuery,
  splitTopLevelAndClauses,
} from '../lib/searchQueryParser';
import { FieldSidebarComponent } from './FieldSidebarComponent';
import { FilterChipsComponent } from './FilterChipsComponent';
import { FloatingTopButtonComponent } from './FloatingTopButtonComponent';
import { HowToSendLogsFromCodeComponent } from './HowToSendLogsFromCodeComponent';
import { LogAnomaliesComponent } from './LogAnomaliesComponent';
import { LogHistogramComponent } from './LogHistogramComponent';
import { OnboardingTooltipComponent } from './OnboardingTooltipComponent';
import { QueryBuilderComponent } from './QueryBuilderComponent';
import { QueryResultsComponent } from './QueryResultsComponent';
import { SQLResultsTableComponent } from './SQLResultsTableComponent';
import { SearchBarComponent, type SearchMode } from './SearchBarComponent';
import {
  DEFAULT_TIME_PRESET,
  type TimeRange,
  type TimeRangeHelpers,
  TimeRangePickerComponent,
  type TimeRangeSelection,
} from './TimeRangePickerComponent';

interface Props {
  projectId: string;
  contentHeight: number;
  user?: UserProfile;
  /** Show a log's trace on the Traces page. */
  onOpenTrace?: (traceId: string, timestamp: string) => void;
}

/**
 * The logs page. "Search" mode parses free text client-side into the same
 * QueryNode tree the structured query API runs (the Advanced builder edits
 * that tree directly, one-way); "SQL" mode runs a user-written SELECT and
 * renders a generic table since its result shape varies.
 */

// Hidden until the per-language SDKs its walkthrough covers are ready.
const SHOW_HOW_TO_SEND_LOGS_FROM_CODE = false;

// The virtual message-pattern field (backend logs_core.PatternHashField).
const PATTERN_HASH_FIELD = 'pattern_hash';

const LIVE_TAIL_QUICK_LEVELS = ['error', 'warn', 'info', 'debug'] as const;
// A recent value's widened range starts this long before its last sighting.
const RECENT_VALUE_MARGIN_MINUTES = 15;
const LIVE_TAIL_LEVEL_CHIP_ACTIVE_CLASS =
  'border-emerald-600 bg-emerald-600 hover:border-emerald-700 hover:bg-emerald-700';

interface SavedQuery {
  mode: SearchMode;
  simpleText: string;
  sqlText: string;
  query: QueryNode | null;
  sortOrder: 'asc' | 'desc';
}

export const QueryComponentComponent = ({
  projectId,
  contentHeight,
  user,
  onOpenTrace,
}: Props): React.JSX.Element => {
  const [isShowHowToSendLogsFromCode, setIsShowHowToSendLogsFromCode] = useState(false);
  const [queryableFields, setQueryableFields] = useState<QueryableField[]>([]);

  const [mode, setMode] = useState<SearchMode>('simple');
  const [simpleText, setSimpleText] = useState('');
  const [sqlText, setSqlText] = useState('');
  const [currentQuery, setCurrentQuery] = useState<QueryNode | null>(null);
  const [showAdvancedBuilder, setShowAdvancedBuilder] = useState(false);
  // Levels unchecked in the severity facet; kept out of the search text and
  // ANDed on as effectiveQuery.
  const [hiddenLevels, setHiddenLevels] = useState<ReadonlySet<string>>(new Set());
  // The histogram's cursor line: the hovered or selected result row's time.
  const [cursorTime, setCursorTime] = useState<string | null>(null);
  // Picker selections to go back to after a histogram zoom (most recent last).
  const [zoomStack, setZoomStack] = useState<TimeRangeSelection[]>([]);
  // Bumped to re-run the search once state set in the same event has rendered.
  const [runToken, setRunToken] = useState(0);
  const [savedSearches, setSavedSearches] = useState<SavedSearch[]>([]);
  // The queries behind the results on screen, so the histogram and sidebar
  // match the table and only refetch on a run.
  const [executedQueries, setExecutedQueries] = useState<{
    effective: QueryNode | null;
    base: QueryNode | null;
    /** The levels `effective` leaves out (the histogram hides them itself). */
    hiddenLevels: ReadonlySet<string>;
    timeRange: TimeRange | null;
  }>({ effective: null, base: null, hiddenLevels: new Set(), timeRange: null });
  // Severity facet counts, from the histogram (which ignores the severity filter).
  const [levelCounts, setLevelCounts] = useState<Record<string, number>>({});
  const [isAnomaliesOpen, setIsAnomaliesOpen] = useState(false);
  const [isLevelCountsLoading, setIsLevelCountsLoading] = useState(false);

  const [sortOrder, setSortOrder] = useState<'asc' | 'desc'>('desc');
  const [isExecuting, setIsExecuting] = useState(false);
  const [queryResults, setQueryResults] = useState<LogItem[]>([]);
  const [totalResults, setTotalResults] = useState(0);
  const [sqlResponse, setSqlResponse] = useState<SQLQueryResponse | null>(null);
  const [hasExecuted, setHasExecuted] = useState(false);
  const [hasMoreResults, setHasMoreResults] = useState(false);
  const [frozenTimeRange, setFrozenTimeRange] = useState<TimeRange | null>(null);
  const [pageSize] = useState(200);
  const [hasSearched, setHasSearched] = useState(false);
  const [isInitialLoad, setIsInitialLoad] = useState(true);
  const [project, setProject] = useState<Project | undefined>();
  const [showOnboarding, setShowOnboarding] = useState(false);
  const [histogramRefreshToken, setHistogramRefreshToken] = useState(0);
  const [recentQueries, setRecentQueries] = useState<string[]>([]);
  const [liveTailEnabled, setLiveTailEnabled] = useState(false);
  // null until known; 'poll' when no push path works (e.g. a buffering proxy).
  const [liveTailMode, setLiveTailMode] = useState<'push' | 'poll' | null>(null);
  // Pause freezes the list but keeps the connection; new logs buffer in
  // liveTailBufferRef until Resume.
  const [liveTailPaused, setLiveTailPaused] = useState(false);
  const [liveTailPausedCount, setLiveTailPausedCount] = useState(0);
  const [liveTailGapMessage, setLiveTailGapMessage] = useState<string | null>(null);
  // Persistent rather than a toast so the user can glance back at it.
  const [resultSummary, setResultSummary] = useState<string | null>(null);

  const timeRangeRef = useRef<() => TimeRange | null>(null);
  // The Advanced builder's query while it owns the search; currentQuery lags
  // the text by a render.
  const builderQueryRef = useRef<QueryNode | null>(null);
  const timeRangeHelpersRef = useRef<TimeRangeHelpers | null>(null);
  const containerRef = useRef<HTMLDivElement>(null);
  const queryBuilderRef = useRef<HTMLDivElement>(null);
  const howToSendLogsButtonRef = useRef<HTMLDivElement>(null);
  // Refs so the live tail callbacks see the latest query without being recreated.
  const liveTailQueryRef = useRef<QueryNode | null>(null);
  // Newest log timestamp seen by either path; the next poll and the
  // reconnect backfill start from it.
  const liveTailSinceRef = useRef<string | null>(null);
  const liveTailIntervalRef = useRef<ReturnType<typeof setInterval> | null>(null);
  const liveTailErrorCountRef = useRef(0);
  const pushLiveTailRef = useRef<PushLiveTailHandle | null>(null);
  // On push failure we poll, but keep retrying push with backoff.
  const liveTailPushRetryTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null);
  const liveTailPushRetryCountRef = useRef(0);
  // Only reconnects need a backfill; the first connect was seeded by startLiveTail.
  const liveTailHasConnectedRef = useRef(false);
  // The push onLog closure outlives renders, so it reads pause state from a ref.
  const liveTailPausedRef = useRef(false);
  const liveTailBufferRef = useRef<LogItem[]>([]);
  // Current results length for Load More, immune to stale closures.
  const queryResultsRef = useRef<LogItem[]>([]);
  // Load More continues from here; offsets repeated rows when new logs
  // arrived at the top between pages.
  const nextCursorRef = useRef<string | undefined>(undefined);
  // Only the latest executeQuery call may apply its response, so
  // out-of-order responses can't clobber fresher results.
  const executeQuerySeqRef = useRef(0);
  // The running search's stream: a new search (or leaving the page) aborts it.
  const searchAbortRef = useRef<AbortController | null>(null);

  const { message } = App.useApp();

  const isUserNewlyRegistered = (user: UserProfile): boolean => {
    const fiveMinutesAgo = new Date(Date.now() - 5 * 60 * 1000);
    return new Date(user.createdAt) > fiveMinutesAgo;
  };

  const shouldShowOnboarding = (): boolean => {
    // The tooltip points at the hidden "How to send logs from code?" button.
    if (!SHOW_HOW_TO_SEND_LOGS_FROM_CODE) return false;
    if (!user) return false;
    if (!isUserNewlyRegistered(user)) return false;
    const onboardingShown = localStorage.getItem('obzly-onboarding-shown');
    return !onboardingShown;
  };

  const handleDismissOnboarding = () => {
    localStorage.setItem('obzly-onboarding-shown', 'true');
    setShowOnboarding(false);
  };

  const handleHowToSendLogsClick = () => {
    if (showOnboarding) {
      handleDismissOnboarding();
    }
    setIsShowHowToSendLogsFromCode(!isShowHowToSendLogsFromCode);
  };

  const getSavedQueryKey = (projectId: string): string => {
    return `obzly-query-${projectId}`;
  };

  const saveQueryToStorage = (saved: SavedQuery) => {
    try {
      localStorage.setItem(getSavedQueryKey(projectId), JSON.stringify(saved));
    } catch (error) {
      console.warn('Failed to save query to localStorage:', error);
    }
  };

  const loadQueryFromStorage = (): SavedQuery | null => {
    try {
      const saved = localStorage.getItem(getSavedQueryKey(projectId));
      if (saved) {
        return JSON.parse(saved) as SavedQuery;
      }
    } catch (error) {
      console.warn('Failed to load query from localStorage:', error);
    }
    return null;
  };

  const loadProject = async () => {
    try {
      const projectData = await projectApi.getProject(projectId);
      setProject(projectData);
    } catch (error: unknown) {
      const errorMessage = error instanceof Error ? error.message : 'Failed to load project';
      message.error(errorMessage);
    }
  };

  const loadQueryableFields = async () => {
    try {
      const response = await queryApi.getQueryableFields(projectId);
      setQueryableFields(response.fields);
    } catch (error: unknown) {
      const errorMessage =
        error instanceof Error ? error.message : 'Failed to load queryable fields';
      message.error(errorMessage);
    }
  };

  const searchQueryableFields = async (searchTerm?: string): Promise<QueryableField[]> => {
    try {
      const request: GetQueryableFieldsRequest | undefined = searchTerm
        ? { query: searchTerm }
        : undefined;
      const response = await queryApi.getQueryableFields(projectId, request);
      return response.fields;
    } catch (error: unknown) {
      const errorMessage =
        error instanceof Error ? error.message : 'Failed to search queryable fields';
      message.error(errorMessage);
      return [];
    }
  };

  useEffect(() => () => searchAbortRef.current?.abort(), []);

  // An invalid in-progress expression keeps the last valid query.
  useEffect(() => {
    if (mode !== 'simple') return;
    try {
      setCurrentQuery(parseSearchQuery(simpleText));
    } catch (e: unknown) {
      if (!(e instanceof SearchQuerySyntaxError)) throw e;
      // SearchBarComponent shows the syntax error.
    }
  }, [mode, simpleText]);

  useEffect(() => {
    queryResultsRef.current = queryResults;
  }, [queryResults]);

  // Same reference as currentQuery when no level is hidden, so nothing refetches.
  const effectiveQuery = useMemo(
    () => withHiddenLevels(currentQuery, hiddenLevels),
    [currentQuery, hiddenLevels],
  );

  const resolveTimeRangeISO = (isLoadMore: boolean): TimeRange | null => {
    if (isLoadMore && frozenTimeRange) return frozenTimeRange;

    const current = timeRangeRef.current?.() ?? null;
    if (current) {
      setFrozenTimeRange(current);
    }
    return current;
  };

  // Any edit to the search text takes the search back from the Advanced builder.
  const setSearchText: typeof setSimpleText = (value) => {
    builderQueryRef.current = null;
    setSimpleText(value);
  };

  const resolveBaseQuery = (): QueryNode | null => {
    if (mode !== 'simple') return currentQuery;
    if (!simpleText.trim()) {
      // Not currentQuery: it's a render stale here.
      return builderQueryRef.current;
    }
    try {
      return parseSearchQuery(simpleText);
    } catch {
      return currentQuery; // syntax error: the bar shows it; keep the last valid query
    }
  };

  // A fresh search's "Found N" waits for the histogram's total, which may arrive first.
  const pendingSummaryRef = useRef<{ queryType: string; ms: number } | null>(null);
  const histogramTotalRef = useRef<number | null>(null);

  const foundSummary = (total: number, queryType: string, ms: number) =>
    `Found ${total.toLocaleString()} logs ${queryType} (${ms.toLocaleString()} ms)`;

  const handleHistogramLevelCounts = (counts: Record<string, number> | null) => {
    setLevelCounts(counts ?? {});
    setIsLevelCountsLoading(false);
  };

  const handleHistogramTotal = (total: number | null) => {
    histogramTotalRef.current = total;
    if (total === null) return;
    setTotalResults(total);
    const pending = pendingSummaryRef.current;
    if (pending) {
      pendingSummaryRef.current = null;
      setResultSummary(foundSummary(total, pending.queryType, pending.ms));
    }
  };

  // Keeps the rows already shown.
  const handleStop = () => {
    executeQuerySeqRef.current++;
    searchAbortRef.current?.abort();
    searchAbortRef.current = null;
    pendingSummaryRef.current = null;
    setIsExecuting(false);
    setResultSummary(
      queryResultsRef.current.length > 0
        ? `Search stopped - showing the ${queryResultsRef.current.length.toLocaleString()} logs found so far`
        : 'Search stopped',
    );
  };

  const executeQuery = async (isLoadMore = false) => {
    const seq = ++executeQuerySeqRef.current;
    searchAbortRef.current?.abort();
    const abort = new AbortController();
    searchAbortRef.current = abort;
    setIsExecuting(true);
    if (!isLoadMore) {
      setResultSummary(null);
      pendingSummaryRef.current = null;
    }
    try {
      const baseCount = queryResultsRef.current.length;
      // Parse the text now: currentQuery is synced by an effect, a render
      // behind. Load More reuses the query the loaded rows came from.
      const baseQuery = resolveBaseQuery();
      const queryToRun = isLoadMore
        ? executedQueries.effective
        : withHiddenLevels(baseQuery, hiddenLevels);
      const cursor = isLoadMore ? nextCursorRef.current : undefined;
      // The histogram (simple mode) sums to the total, so the page needn't
      // count: counting reads the whole range, the page only its newest end.
      const totalFromHistogram = mode === 'simple';
      const request: LogQueryRequest = {
        query: queryToRun,
        limit: pageSize,
        offset: isLoadMore && !cursor ? baseCount : 0,
        sortOrder,
        ...(cursor ? { cursor } : {}),
        ...(totalFromHistogram && !isLoadMore ? { trackTotal: false } : {}),
      };

      const timeRangeToUse = resolveTimeRangeISO(isLoadMore);
      if (!timeRangeToUse) {
        // "Custom Range" with no dates picked: the Site needs a range, and
        // the histogram and sidebar would wait for one forever.
        message.error('Select a time range first');
        return;
      }
      request.timeRange = {
        from: timeRangeToUse.from.toISOString(),
        to: timeRangeToUse.to.toISOString(),
      };

      if (!isLoadMore) {
        // Start the histogram (and the total) alongside the search.
        histogramTotalRef.current = null;
        setTotalResults(-1);
        setIsLevelCountsLoading(true);
        setExecutedQueries({
          effective: request.query ?? null,
          base: baseQuery,
          hiddenLevels,
          timeRange: timeRangeToUse,
        });
        setHistogramRefreshToken((t) => t + 1);
      }

      let firstRows = true;
      const showRows = (logs: LogItem[]) => {
        if (seq !== executeQuerySeqRef.current) return;
        if (isLoadMore || !firstRows) {
          setQueryResults((prev) => [...prev, ...logs]);
        } else {
          setQueryResults(logs);
          setHasExecuted(true);
        }
        firstRows = false;
      };

      let response: LogQueryResponse;
      try {
        response = await queryApi.streamQuery(projectId, request, showRows, abort.signal);
      } catch (error: unknown) {
        if (!(error instanceof SearchStreamUnavailableError)) throw error;
        // Older Site, expired token or refused stream: the plain search handles it.
        response = await queryApi.executeQuery(projectId, { ...request, trackTotal: undefined });
        firstRows = true;
        showRows(response.logs);
      }

      // A newer call has started; drop this stale response.
      if (seq !== executeQuerySeqRef.current) return;

      if (!isLoadMore) {
        if (firstRows) setQueryResults([]);
        setHasExecuted(true);
        if (response.total >= 0) setTotalResults(response.total);
      }

      const currentResultsCount = isLoadMore
        ? baseCount + response.logs.length
        : response.logs.length;
      nextCursorRef.current = response.nextCursor;
      // A cursor page isn't counted (total -1); an older Site sends no cursor.
      setHasMoreResults(!!response.nextCursor || currentResultsCount < response.total);

      if (!isLoadMore) {
        const queryType = queryToRun ? 'matching your query' : '(showing all logs)';
        const executedInMs = Math.round(parseFloat(response.executedIn)) || 0;
        const total = response.total >= 0 ? response.total : histogramTotalRef.current;
        if (response.partial) {
          const reached = response.searchedThrough
            ? dayjs(response.searchedThrough).format('MMM D, HH:mm')
            : 'the start';
          setResultSummary(
            `Found ${response.logs.length.toLocaleString()} logs ${queryType} - the search stopped at its time limit, having searched back to ${reached}. Narrow the time range or add a filter to search further.`,
          );
        } else if (response.countSkipped) {
          setResultSummary(
            `Showing the newest logs ${queryType} - too much data in this range to count them (${executedInMs.toLocaleString()} ms)`,
          );
        } else if (total !== null) {
          setResultSummary(foundSummary(total, queryType, executedInMs));
        } else {
          setResultSummary(
            `Showing the newest logs ${queryType} (${executedInMs.toLocaleString()} ms)`,
          );
          pendingSummaryRef.current = { queryType, ms: executedInMs };
        }
        setHasSearched(true);
      }
    } catch (error: unknown) {
      if (seq !== executeQuerySeqRef.current || abort.signal.aborted) return;
      const errorMessage = error instanceof Error ? error.message : 'Query execution failed';
      setResultSummary(null);
      message.error(errorMessage);
    } finally {
      if (seq === executeQuerySeqRef.current) setIsExecuting(false);
    }
  };

  const executeSQL = async () => {
    if (!sqlText.trim()) {
      message.error('Enter a SQL query first');
      return;
    }

    setIsExecuting(true);
    setResultSummary(null);
    try {
      const timeRangeToUse = resolveTimeRangeISO(false);
      if (!timeRangeToUse) {
        message.error('Select a time range first');
        return;
      }

      const response = await queryApi.executeSQLQuery(projectId, {
        sql: sqlText,
        timeRange: {
          from: timeRangeToUse.from.toISOString(),
          to: timeRangeToUse.to.toISOString(),
        },
      });
      setSqlResponse(response);
      setHasExecuted(true);
      setHasSearched(true);

      const executedInMs = Math.round(parseFloat(response.executedInMs));
      setResultSummary(
        `${response.rowCount.toLocaleString()} row${response.rowCount === 1 ? '' : 's'} (${executedInMs.toLocaleString()} ms)`,
      );
    } catch (error: unknown) {
      const errorMessage = error instanceof Error ? error.message : 'SQL query execution failed';
      setResultSummary(null);
      message.error(errorMessage);
    } finally {
      setIsExecuting(false);
    }
  };

  // Called from every path that runs a Simple-mode search.
  const recordCurrentSimpleQuery = () => {
    if (simpleText.trim()) {
      recordRecentQuery(projectId, simpleText);
      setRecentQueries(getRecentQueries(projectId));
    }
  };

  const handleRun = () => {
    if (mode === 'sql') {
      executeSQL();
    } else {
      recordCurrentSimpleQuery();
      executeQuery(false);
    }
  };

  const handleSelectRecentQuery = (text: string) => {
    setMode('simple');
    setShowAdvancedBuilder(false);
    setSearchText(text);
    setHasSearched(false);
  };

  const handleLoadMore = () => {
    executeQuery(true);
  };

  // Live Tail requires a filter; tailing a whole project unfiltered isn't supported.
  const canEnableLiveTail =
    mode === 'simple' && (simpleText.trim() !== '' || currentQuery !== null);

  // Stable dependency so a filter change mid-tail reopens the push
  // connection (the query object's reference changes too often).
  const liveTailQueryKey = useMemo(() => JSON.stringify(effectiveQuery), [effectiveQuery]);

  // Include attributes on loaded rows that field discovery didn't sample.
  const sidebarFields = useMemo(() => {
    const known = new Set(queryableFields.map((field) => field.name));
    const extra = new Set<string>();
    for (const log of queryResults) {
      for (const name of Object.keys(log.fields ?? {})) {
        if (!known.has(name)) extra.add(name);
      }
    }
    return [
      ...queryableFields,
      ...[...extra].map((name): QueryableField => ({
        name,
        type: 'string',
        isCustom: true,
        operations: ['equals', 'not_equals', 'contains', 'not_contains', 'exists', 'not_exists'],
      })),
    ];
  }, [queryableFields, queryResults]);

  useEffect(() => {
    liveTailQueryRef.current = effectiveQuery;
  }, [effectiveQuery]);

  useEffect(() => {
    liveTailPausedRef.current = liveTailPaused;
  }, [liveTailPaused]);

  // While paused, new logs go to liveTailBufferRef instead of the results.
  const mergeIncomingLogs = (newestFirstLogs: LogItem[]) => {
    if (newestFirstLogs.length === 0) return;
    if (liveTailPausedRef.current) {
      liveTailBufferRef.current = [...newestFirstLogs, ...liveTailBufferRef.current].slice(0, 1000);
      setLiveTailPausedCount((c) => c + newestFirstLogs.length);
    } else {
      setQueryResults((prev) => [...newestFirstLogs, ...prev].slice(0, 1000));
      setTotalResults((prev) => prev + newestFirstLogs.length);
    }
  };

  const pollLiveTail = async () => {
    if (!liveTailSinceRef.current) return;
    const from = liveTailSinceRef.current;
    const to = new Date().toISOString();
    try {
      const response = await queryApi.executeQuery(projectId, {
        query: liveTailQueryRef.current,
        timeRange: { from, to },
        limit: 500,
        offset: 0,
        sortOrder: 'asc',
      });
      liveTailErrorCountRef.current = 0;
      if (response.logs.length === 0) return;

      // "from" is inclusive; +1ms avoids re-fetching the newest log.
      const newestTimestamp = response.logs[response.logs.length - 1].timestamp;
      liveTailSinceRef.current = new Date(new Date(newestTimestamp).getTime() + 1).toISOString();

      mergeIncomingLogs([...response.logs].reverse());
    } catch {
      // Give up only after several consecutive failures.
      liveTailErrorCountRef.current += 1;
      if (liveTailErrorCountRef.current >= 5) {
        message.error('Live tail stopped: repeated errors fetching new logs');
        setLiveTailEnabled(false);
      }
    }
  };

  // Seeds the view with the last minute of logs; the tail continues from
  // startedAt with no gap or overlap.
  const startLiveTail = async () => {
    const startedAt = new Date();
    liveTailSinceRef.current = startedAt.toISOString();
    liveTailErrorCountRef.current = 0;
    setHasExecuted(true);
    setHasSearched(true);
    setResultSummary('Live tail active…');

    try {
      const response = await queryApi.executeQuery(projectId, {
        query: liveTailQueryRef.current,
        timeRange: {
          from: new Date(startedAt.getTime() - 60_000).toISOString(),
          to: startedAt.toISOString(),
        },
        limit: 200,
        offset: 0,
        sortOrder: 'desc',
      });
      setQueryResults(response.logs);
      setTotalResults(response.logs.length);
    } catch {
      // Best-effort; the tail itself still fills the view.
      setQueryResults([]);
      setTotalResults(0);
    }
  };

  // The 2-second poll fallback when push isn't working; idempotent.
  const startPolling = () => {
    if (liveTailIntervalRef.current) return;
    setLiveTailMode('poll');
    liveTailIntervalRef.current = setInterval(pollLiveTail, 2000);
  };

  const stopPolling = () => {
    if (liveTailIntervalRef.current) {
      clearInterval(liveTailIntervalRef.current);
      liveTailIntervalRef.current = null;
    }
  };

  const clearPushRetryTimer = () => {
    if (liveTailPushRetryTimerRef.current) {
      clearTimeout(liveTailPushRetryTimerRef.current);
      liveTailPushRetryTimerRef.current = null;
    }
  };

  // Valkey Pub/Sub doesn't replay, so after a reconnect we query the gap
  // and tell the user the outcome.
  const backfillGap = async () => {
    if (!liveTailSinceRef.current) return;
    const from = liveTailSinceRef.current;
    const to = new Date().toISOString();
    try {
      const response = await queryApi.executeQuery(projectId, {
        query: liveTailQueryRef.current,
        timeRange: { from, to },
        limit: 500,
        offset: 0,
        sortOrder: 'asc',
      });
      if (response.logs.length > 0) {
        const newestTimestamp = response.logs[response.logs.length - 1].timestamp;
        liveTailSinceRef.current = new Date(new Date(newestTimestamp).getTime() + 1).toISOString();
        mergeIncomingLogs([...response.logs].reverse());
        setLiveTailGapMessage(
          `Reconnected — backfilled ${response.logs.length.toLocaleString()} log${response.logs.length === 1 ? '' : 's'} from the gap`,
        );
      } else {
        setLiveTailGapMessage('Reconnected — no logs missed');
      }
    } catch {
      setLiveTailGapMessage('Reconnected — a short gap may not be fully backfilled');
    }
    setTimeout(() => setLiveTailGapMessage(null), 5000);
  };

  // After these retries we stay on polling for the session.
  const PUSH_RETRY_DELAYS_MS = [5000, 15000, 30000, 30000, 30000];

  const schedulePushRetry = () => {
    const attempt = liveTailPushRetryCountRef.current;
    if (attempt >= PUSH_RETRY_DELAYS_MS.length) return;
    liveTailPushRetryCountRef.current += 1;
    liveTailPushRetryTimerRef.current = setTimeout(connectPush, PUSH_RETRY_DELAYS_MS[attempt]);
  };

  // Push first; polling is only the fallback. Also the retry entry point.
  const connectPush = () => {
    // Safe to call re-entrantly.
    pushLiveTailRef.current?.stop();
    clearPushRetryTimer();

    pushLiveTailRef.current = startPushLiveTail(projectId, liveTailQueryRef.current, {
      onReady: () => {
        stopPolling(); // a slow push confirmation may have raced the fallback poll
        clearPushRetryTimer();
        liveTailPushRetryCountRef.current = 0;
        setLiveTailMode('push');

        if (liveTailHasConnectedRef.current) {
          void backfillGap();
        }
        liveTailHasConnectedRef.current = true;
      },
      onLog: (log) => {
        liveTailSinceRef.current = new Date(new Date(log.timestamp).getTime() + 1).toISOString();
        mergeIncomingLogs([log]);
      },
      onError: () => {
        pushLiveTailRef.current = null;
        startPolling();
        schedulePushRetry();
      },
    });
  };

  useEffect(() => {
    if (!liveTailEnabled) {
      stopPolling();
      pushLiveTailRef.current?.stop();
      pushLiveTailRef.current = null;
      clearPushRetryTimer();
      liveTailPushRetryCountRef.current = 0;
      liveTailHasConnectedRef.current = false;
      setLiveTailMode(null);
      setLiveTailPaused(false);
      setLiveTailPausedCount(0);
      liveTailBufferRef.current = [];
      setLiveTailGapMessage(null);
      return;
    }

    startLiveTail();
    liveTailHasConnectedRef.current = false;
    connectPush();

    return () => {
      pushLiveTailRef.current?.stop();
      pushLiveTailRef.current = null;
      clearPushRetryTimer();
      stopPolling();
    };
  }, [liveTailEnabled, projectId, liveTailQueryKey]);

  const handleToggleLiveTail = (enabled: boolean) => {
    setLiveTailEnabled(enabled);
  };

  const handlePauseLiveTail = () => {
    setLiveTailPaused(true);
  };

  const handleResumeLiveTail = () => {
    setLiveTailPaused(false);
    if (liveTailBufferRef.current.length > 0) {
      const buffered = liveTailBufferRef.current;
      liveTailBufferRef.current = [];
      setQueryResults((prev) => [...buffered, ...prev].slice(0, 1000));
      setTotalResults((prev) => prev + buffered.length);
    }
    setLiveTailPausedCount(0);
  };

  const handleClearLiveTail = () => {
    setQueryResults([]);
    setTotalResults(0);
  };

  // Turn Live Tail off if its filter is cleared or the mode leaves Simple.
  useEffect(() => {
    if (liveTailEnabled && !canEnableLiveTail) {
      setLiveTailEnabled(false);
    }
  }, [liveTailEnabled, canEnableLiveTail]);

  // Toggles a clause in a flat AND search, replacing its opposite
  // (field:value <-> -field:value) so repeat clicks don't stack. Other
  // searches (OR/NOT/parens) just get it appended.
  const applyFilterClause = (clause: string, opposite: string) => {
    setMode('simple');
    setShowAdvancedBuilder(false);
    setSearchText((prev) => {
      const trimmed = prev.trim();
      const existing = splitTopLevelAndClauses(trimmed);
      if (existing) {
        if (existing.includes(clause)) return existing.filter((c) => c !== clause).join(' AND ');
        return [...existing.filter((c) => c !== opposite), clause].join(' AND ');
      }
      return trimmed ? `${trimmed} AND ${clause}` : clause;
    });
    requestRun();
  };

  const handleAddFilter = (fieldName: string, fieldValue: string) => {
    const clause = `${fieldName}:${quoteIfNeeded(fieldValue)}`;
    applyFilterClause(clause, `-${clause}`);
  };

  // A value the sidebar offers from outside the range: the range first widens
  // to its last sighting (undone with Zoom out, like a histogram zoom).
  const handleAddRecentFilter = (fieldName: string, fieldValue: string, lastSeen: string) => {
    const helpers = timeRangeHelpersRef.current;
    const range = executedQueries.timeRange;
    const seen = dayjs(lastSeen);
    if (
      !liveTailEnabled &&
      helpers &&
      range &&
      (seen.isBefore(range.from) || seen.isAfter(range.to))
    ) {
      const from = seen.subtract(RECENT_VALUE_MARGIN_MINUTES, 'minute');
      const to = seen.add(1, 'minute');
      setZoomStack((stack) => [...stack, helpers.getSelection()]);
      helpers.applyCustomRange({
        from: from.isBefore(range.from) ? from : range.from,
        to: to.isAfter(range.to) ? to : range.to,
      });
    }
    handleAddFilter(fieldName, fieldValue);
  };

  const handleExcludeFilter = (fieldName: string, fieldValue: string) => {
    const clause = `${fieldName}:${quoteIfNeeded(fieldValue)}`;
    applyFilterClause(`-${clause}`, clause);
  };

  const handleRemoveClause = (clause: string) => {
    setSearchText((prev) =>
      (splitTopLevelAndClauses(prev.trim()) ?? []).filter((c) => c !== clause).join(' AND '),
    );
    requestRun();
  };

  // Back to an unfiltered search over the pre-zoom time range.
  const handleReset = () => {
    if (isExecuting) handleStop();
    if (mode === 'sql') {
      setSqlText('');
      setResultSummary(null);
      return;
    }
    setSearchText('');
    setCurrentQuery(null);
    setShowAdvancedBuilder(false);
    setSortOrder('desc');
    // A "Custom Range" without dates falls back to the default preset.
    const helpers = timeRangeHelpersRef.current;
    if (helpers) {
      const target = zoomStack[0] ?? helpers.getSelection();
      if (target.preset === 'custom' && !target.customRange) {
        helpers.restoreSelection({ preset: DEFAULT_TIME_PRESET, customRange: null });
      } else if (zoomStack.length > 0) {
        helpers.restoreSelection(target);
      }
    }
    setZoomStack([]);
    if (hiddenLevels.size > 0) {
      setHiddenLevels(new Set()); // its own effect re-runs the search
    } else {
      requestRun();
    }
  };

  const handleClearAllFilters = () => {
    setSearchText('');
    if (hiddenLevels.size > 0) {
      setHiddenLevels(new Set()); // its own effect re-runs the search
    } else {
      requestRun();
    }
  };

  const handleSaveSearch = (name: string) => {
    saveSearch(projectId, { name, mode, text: mode === 'sql' ? sqlText : simpleText });
    setSavedSearches(getSavedSearches(projectId));
    message.success(`Saved "${name}"`);
  };

  const handleSelectSavedSearch = (saved: SavedSearch) => {
    setMode(saved.mode);
    setShowAdvancedBuilder(false);
    if (saved.mode === 'sql') setSqlText(saved.text);
    else setSearchText(saved.text);
    requestRun();
  };

  const handleDeleteSavedSearch = (name: string) => {
    deleteSavedSearch(projectId, name);
    setSavedSearches(getSavedSearches(projectId));
  };

  // Re-runs via requestRun: timeRangeRef only updates in the picker's
  // effect after it re-renders with the new range.
  const handleHistogramZoom = ({ from, to }: { from: number; to: number }) => {
    const helpers = timeRangeHelpersRef.current;
    if (!helpers) return;
    setZoomStack((stack) => [...stack, helpers.getSelection()]);
    helpers.applyCustomRange({ from: dayjs(from), to: dayjs(to) });
    requestRun();
  };

  // Undone with Zoom out like a histogram zoom; hidden levels are cleared
  // since the context showed them all.
  const handleOpenContextAsSearch = (
    scope: LogContextScope[],
    from: dayjs.Dayjs,
    to: dayjs.Dayjs,
  ) => {
    const helpers = timeRangeHelpersRef.current;
    if (!helpers) return;
    if (liveTailEnabled) setLiveTailEnabled(false);
    setMode('simple');
    setShowAdvancedBuilder(false);
    setSearchText(scope.map((s) => `${s.field}=${quoteIfNeeded(s.value)}`).join(' AND '));
    setZoomStack((stack) => [...stack, helpers.getSelection()]);
    helpers.applyCustomRange({ from, to });
    if (hiddenLevels.size > 0) {
      setHiddenLevels(new Set()); // its own effect re-runs the search
    } else {
      requestRun();
    }
  };

  const handleZoomOut = () => {
    const helpers = timeRangeHelpersRef.current;
    const previous = zoomStack[zoomStack.length - 1];
    if (!helpers || !previous) return;
    setZoomStack((stack) => stack.slice(0, -1));
    helpers.restoreSelection(previous);
    requestRun();
  };

  // Runs the search after pending state has rendered. Live Tail restarts
  // its own stream instead.
  const requestRun = () => setRunToken((t) => t + 1);
  useEffect(() => {
    if (runToken === 0) return;
    if (mode === 'sql') void executeSQL();
    else if (!liveTailEnabled) void executeQuery();
    // Only runToken should trigger this; the run reads current state.
  }, [runToken]);

  const handleToggleLevel = (level: string, visible: boolean) => {
    setHiddenLevels((prev) => {
      const next = new Set(prev);
      if (visible) next.delete(level);
      else next.add(level);
      return next;
    });
  };

  // "only" on the level that's already the only one shows everything again.
  const handleShowOnlyLevel = (level: string, listedLevels: string[]) => {
    setHiddenLevels((prev) => {
      const everyOther = listedLevels.filter((l) => l !== level);
      const alreadyOnly = !prev.has(level) && everyOther.every((l) => prev.has(l));
      return alreadyOnly ? new Set() : new Set(everyOther);
    });
  };

  // A severity-facet change re-runs the search; Live Tail restarts via liveTailQueryKey.
  const previousHiddenLevelsRef = useRef(hiddenLevels);
  useEffect(() => {
    if (previousHiddenLevelsRef.current === hiddenLevels) return;
    previousHiddenLevelsRef.current = hiddenLevels;
    if (mode === 'simple' && hasExecuted && !liveTailEnabled) {
      void executeQuery();
    }
    // Only a hiddenLevels change should trigger this.
  }, [hiddenLevels]);

  const handleExecuteOrRefresh = async () => {
    if (mode === 'sql') {
      executeSQL();
      return;
    }

    recordCurrentSimpleQuery();

    if (hasSearched) {
      const helpers = timeRangeHelpersRef.current;
      if (helpers?.isUntilNow()) {
        helpers.refreshRange();
        setHasSearched(false);
        setTimeout(() => executeQuery(false), 50);
      } else {
        executeQuery(false);
      }
    } else {
      executeQuery(false);
    }
  };

  useEffect(() => {
    const initializeProject = async () => {
      await Promise.all([loadProject(), loadQueryableFields()]);
      setRecentQueries(getRecentQueries(projectId));
      setSavedSearches(getSavedSearches(projectId));

      const savedQuery = loadQueryFromStorage();
      if (savedQuery) {
        setMode(savedQuery.mode ?? 'simple');
        setSimpleText(savedQuery.simpleText ?? '');
        builderQueryRef.current = savedQuery.simpleText ? null : (savedQuery.query ?? null);
        setSqlText(savedQuery.sqlText ?? '');
        setCurrentQuery(savedQuery.query);
        setSortOrder(savedQuery.sortOrder ?? 'desc');
      } else {
        setMode('simple');
        setSearchText('');
        setSqlText('');
        setCurrentQuery(null);
        setSortOrder('desc');
      }

      setQueryResults([]);
      setTotalResults(0);
      setSqlResponse(null);
      setHasExecuted(false);
      setHasMoreResults(false);
      nextCursorRef.current = undefined;
      setFrozenTimeRange(null);
      setHasSearched(false);
      setResultSummary(null);
      setLiveTailEnabled(false);

      setIsInitialLoad(false);
    };

    initializeProject();
    clearValueSuggestions();
  }, [projectId]);

  // Search bar value completion: a field's top values in the newest logs.
  const loadFieldValues = async (field: string): Promise<ValueSuggestion[]> => {
    const range = timeRangeRef.current?.();
    if (!range) return [];
    const response = await queryApi.getFieldValues(projectId, {
      field,
      limit: 20,
      sampleRows: 100_000,
      timeRange: { from: dayjs(range.from).toISOString(), to: dayjs(range.to).toISOString() },
      includeRecent: true,
    });
    return [
      ...response.values.map((v) => ({ value: v.value, count: v.count })),
      ...(response.recent ?? []).map((v) => ({
        value: v.value,
        detail: v.inRange ? 'in range' : `seen ${dayjs(v.lastSeen).fromNow()}`,
      })),
    ];
  };

  useEffect(() => {
    if (!isInitialLoad && queryableFields.length > 0) {
      const timer = setTimeout(() => {
        if (timeRangeRef.current && timeRangeRef.current()) {
          handleRun();
        }
      }, 100);

      return () => clearTimeout(timer);
    }
  }, [isInitialLoad, queryableFields.length]);

  useEffect(() => {
    if (!isInitialLoad) {
      saveQueryToStorage({ mode, simpleText, sqlText, query: currentQuery, sortOrder });
    }
  }, [mode, simpleText, sqlText, currentQuery, sortOrder, projectId, isInitialLoad]);

  useEffect(() => {
    if (!isInitialLoad && user) {
      const timer = setTimeout(() => {
        if (shouldShowOnboarding()) {
          setShowOnboarding(true);
        }
      }, 1000);

      return () => clearTimeout(timer);
    }
  }, [isInitialLoad, user]);

  return (
    <div
      ref={containerRef}
      className="ml-3 w-full space-y-3 overflow-y-auto"
      style={{ height: contentHeight }}
    >
      <FloatingTopButtonComponent containerRef={containerRef} />

      <div
        ref={queryBuilderRef}
        className="w-full space-y-1.5 rounded-lg border border-gray-200 bg-white px-3 py-2 shadow-sm dark:border-gray-700 dark:bg-gray-800"
      >
        <SearchBarComponent
          mode={mode}
          onModeChange={(newMode) => {
            setMode(newMode);
            setHasSearched(false);
          }}
          simpleText={simpleText}
          onSimpleTextChange={(text) => {
            setSearchText(text);
            setHasSearched(false);
          }}
          sqlText={sqlText}
          onSqlTextChange={(text) => {
            setSqlText(text);
            setHasSearched(false);
          }}
          isExecuting={isExecuting}
          onRun={handleExecuteOrRefresh}
          onStop={handleStop}
          onReset={handleReset}
          recentQueries={recentQueries}
          onSelectRecentQuery={handleSelectRecentQuery}
          savedSearches={savedSearches}
          onSaveSearch={handleSaveSearch}
          onSelectSavedSearch={handleSelectSavedSearch}
          onDeleteSavedSearch={handleDeleteSavedSearch}
          fields={queryableFields}
          loadFieldValues={loadFieldValues}
          controls={
            <>
              <div className={liveTailEnabled ? 'pointer-events-none opacity-50' : undefined}>
                <TimeRangePickerComponent
                  compact
                  onChange={() => {
                    setHasSearched(false);
                  }}
                  onGetCurrentRange={(getCurrentRange: () => TimeRange | null) => {
                    // Ref only: setState here loops forever, since this inline
                    // callback is a dependency of the picker's effect.
                    timeRangeRef.current = getCurrentRange;
                  }}
                  onGetRangeHelpers={(helpers) => {
                    timeRangeHelpersRef.current = helpers;
                  }}
                />
              </div>
              {mode === 'simple' && (
                <Tooltip
                  title={
                    sortOrder === 'desc'
                      ? 'Newest first - click for oldest first'
                      : 'Oldest first - click for newest first'
                  }
                >
                  <Button
                    icon={
                      sortOrder === 'desc' ? <SortDescendingOutlined /> : <SortAscendingOutlined />
                    }
                    disabled={liveTailEnabled}
                    aria-label="Toggle sort order"
                    onClick={() => {
                      setSortOrder((o) => (o === 'desc' ? 'asc' : 'desc'));
                      setHasSearched(false);
                    }}
                  />
                </Tooltip>
              )}
              {mode === 'simple' && (
                <Tooltip
                  title={
                    liveTailEnabled
                      ? 'Stop live tail'
                      : canEnableLiveTail
                        ? 'Live tail - stream new matching logs as they arrive'
                        : 'Enter a search filter first to enable live tail'
                  }
                >
                  <Button
                    danger={liveTailEnabled}
                    icon={liveTailEnabled ? <PauseCircleOutlined /> : <PlayCircleOutlined />}
                    disabled={!liveTailEnabled && !canEnableLiveTail}
                    onClick={() => handleToggleLiveTail(!liveTailEnabled)}
                  >
                    Live
                  </Button>
                </Tooltip>
              )}
            </>
          }
        />

        {project?.plan?.warningText && (
          <div className="text-xs text-orange-600 opacity-80">{project.plan.warningText}</div>
        )}

        <div className="flex min-h-[22px] flex-wrap items-center justify-between gap-x-3 gap-y-1 text-xs">
          <div className="flex min-w-0 flex-wrap items-center gap-2">
            {mode === 'simple' && (
              <FilterChipsComponent
                clauses={splitTopLevelAndClauses(simpleText.trim())}
                hiddenLevels={hiddenLevels}
                onRemoveClause={handleRemoveClause}
                onShowLevel={(level) => handleToggleLevel(level, true)}
                onClearAll={handleClearAllFilters}
              />
            )}
            {mode === 'simple' && liveTailEnabled && (
              <div className="flex flex-wrap items-center gap-1.5">
                {liveTailEnabled && (
                  <Tooltip
                    title={
                      liveTailMode === 'poll'
                        ? 'Streaming isn’t reaching this browser (likely a proxy buffering the connection) - refreshing every 2s instead. See the deployment docs’ Live Tail section.'
                        : 'Streaming new logs in real time'
                    }
                  >
                    <span className="flex items-center gap-1 text-xs font-semibold text-red-500">
                      <span className="h-1.5 w-1.5 animate-pulse rounded-full bg-red-500" />
                      LIVE{liveTailMode === 'poll' ? ' (polling)' : ''}
                    </span>
                  </Tooltip>
                )}
                {liveTailEnabled && (
                  <Tooltip
                    title={
                      liveTailPaused
                        ? `Resume - ${liveTailPausedCount} new log${liveTailPausedCount === 1 ? '' : 's'} waiting`
                        : 'Pause - freeze the view without stopping the connection'
                    }
                  >
                    <Button
                      size="small"
                      icon={liveTailPaused ? <PlayCircleOutlined /> : <PauseCircleOutlined />}
                      onClick={liveTailPaused ? handleResumeLiveTail : handlePauseLiveTail}
                    >
                      {liveTailPaused ? `Resume (${liveTailPausedCount})` : 'Pause'}
                    </Button>
                  </Tooltip>
                )}
                {liveTailEnabled && (
                  <Tooltip title="Clear the displayed logs without stopping live tail">
                    <Button size="small" onClick={handleClearLiveTail}>
                      Clear
                    </Button>
                  </Tooltip>
                )}
                {liveTailEnabled && liveTailGapMessage && (
                  <span className="text-xs text-gray-500 dark:text-gray-300">
                    {liveTailGapMessage}
                  </span>
                )}

                <span className="text-xs text-gray-500 dark:text-gray-300">Quick filter:</span>
                {LIVE_TAIL_QUICK_LEVELS.map((level) => {
                  const isActive = simpleText.includes(`level:${level}`);
                  return (
                    <Button
                      key={level}
                      size="small"
                      type={isActive ? 'primary' : 'default'}
                      className={isActive ? LIVE_TAIL_LEVEL_CHIP_ACTIVE_CLASS : undefined}
                      onClick={() => handleAddFilter('level', level)}
                    >
                      {level}
                    </Button>
                  );
                })}
              </div>
            )}
          </div>

          <div className="ml-auto flex items-center gap-3">
            <span className="text-gray-500 dark:text-gray-300">
              {isExecuting ? (
                'Running query…'
              ) : (
                <>
                  {resultSummary}
                  {resultSummary && mode === 'simple' && executedQueries.timeRange && (
                    <span className="ml-1 text-gray-400">
                      · {executedQueries.timeRange.from.format('MMM D, HH:mm:ss')} →{' '}
                      {executedQueries.timeRange.to.format('MMM D, HH:mm:ss')}
                    </span>
                  )}
                </>
              )}
            </span>

            {mode === 'simple' && (
              <Button
                type="link"
                size="small"
                className="!px-0 text-xs"
                icon={showAdvancedBuilder ? <DownOutlined /> : <RightOutlined />}
                onClick={() => setShowAdvancedBuilder((v) => !v)}
              >
                Advanced filters
              </Button>
            )}

            {SHOW_HOW_TO_SEND_LOGS_FROM_CODE && (
              <div ref={howToSendLogsButtonRef}>
                <Button
                  type="primary"
                  size="small"
                  onClick={handleHowToSendLogsClick}
                  ghost
                  className="border-emerald-600 bg-emerald-600 hover:border-emerald-700 hover:bg-emerald-700"
                >
                  How to send logs from code?
                </Button>
              </div>
            )}
          </div>
        </div>

        {mode === 'simple' && showAdvancedBuilder && (
          <div className="rounded border border-gray-100 bg-gray-50 p-4 dark:bg-gray-900">
            <QueryBuilderComponent
              fields={queryableFields}
              query={currentQuery}
              onChange={(query) => {
                // One-way: the search text isn't regenerated from the tree.
                builderQueryRef.current = query;
                setSimpleText('');
                setCurrentQuery(query);
                setHasSearched(false);
              }}
              onFieldSearch={searchQueryableFields}
            />
          </div>
        )}
      </div>

      {mode === 'simple' && hasExecuted && executedQueries.timeRange && !liveTailEnabled && (
        <div className="h-48 w-full rounded-lg border border-gray-200 bg-white p-2 shadow-sm dark:border-gray-700 dark:bg-gray-800">
          <LogHistogramComponent
            projectId={projectId}
            query={executedQueries.base}
            excludedLevels={executedQueries.hiddenLevels}
            timeRange={executedQueries.timeRange}
            refreshToken={histogramRefreshToken}
            cursorTime={cursorTime}
            onZoom={handleHistogramZoom}
            onZoomOut={zoomStack.length > 0 ? handleZoomOut : undefined}
            onTotal={handleHistogramTotal}
            onLevelCounts={handleHistogramLevelCounts}
            onAnomalies={() => setIsAnomaliesOpen(true)}
          />
        </div>
      )}

      <LogAnomaliesComponent
        projectId={projectId}
        open={isAnomaliesOpen}
        query={executedQueries.base}
        timeRange={executedQueries.timeRange}
        onClose={() => setIsAnomaliesOpen(false)}
        onShowPattern={(hash) => {
          setIsAnomaliesOpen(false);
          handleAddFilter(PATTERN_HASH_FIELD, hash);
        }}
      />

      {mode === 'simple' ? (
        <div className="flex w-full items-start gap-3">
          {queryableFields.length > 0 && (
            <FieldSidebarComponent
              projectId={projectId}
              fields={sidebarFields}
              query={executedQueries.effective}
              levelCounts={levelCounts}
              isLevelCountsLoading={isLevelCountsLoading}
              timeRange={executedQueries.timeRange}
              refreshToken={histogramRefreshToken}
              hiddenLevels={hiddenLevels}
              onToggleLevel={handleToggleLevel}
              onShowOnlyLevel={handleShowOnlyLevel}
              onAddFilter={handleAddFilter}
              onAddRecentFilter={handleAddRecentFilter}
            />
          )}
          <div className="min-w-0 flex-1">
            <QueryResultsComponent
              queryResults={queryResults}
              totalResults={totalResults}
              hasExecuted={hasExecuted}
              isExecuting={isExecuting}
              hasMoreResults={!liveTailEnabled && hasMoreResults}
              onLoadMore={handleLoadMore}
              onAddFieldToQuery={handleAddFilter}
              onExcludeFieldFromQuery={handleExcludeFilter}
              onCursorTimeChange={setCursorTime}
              onIncludePattern={(hash) => handleAddFilter(PATTERN_HASH_FIELD, hash)}
              onExcludePattern={(hash) => handleExcludeFilter(PATTERN_HASH_FIELD, hash)}
              projectId={projectId}
              onOpenContextAsSearch={handleOpenContextAsSearch}
              onOpenTrace={onOpenTrace}
            />
          </div>
        </div>
      ) : (
        hasExecuted && <SQLResultsTableComponent response={sqlResponse} isExecuting={isExecuting} />
      )}

      {isShowHowToSendLogsFromCode && (
        <HowToSendLogsFromCodeComponent
          projectId={projectId}
          onClose={() => setIsShowHowToSendLogsFromCode(false)}
        />
      )}

      <OnboardingTooltipComponent targetRef={howToSendLogsButtonRef} show={showOnboarding} />
    </div>
  );
};

function quoteIfNeeded(value: string): string {
  if (value === '' || /[\s"():]/.test(value)) {
    return `"${value.replace(/\\/g, '\\\\').replace(/"/g, '\\"')}"`;
  }
  return value;
}
