import { getApiBase } from '../../../constants';
import RequestOptions from '../../../shared/api/RequestOptions';
import { apiHelper } from '../../../shared/api/apiHelper';
import {
  StreamStalledError,
  StreamUnavailableError,
  streamNdjson,
} from '../../../shared/api/ndjsonStream';
import type { DeploysRequest, DeploysResponse } from '../model/Deploys';
import type { FieldValuesRequest, FieldValuesResponse } from '../model/FieldValues';
import type { GetQueryableFieldsRequest } from '../model/GetQueryableFieldsRequest';
import type { GetQueryableFieldsResponse } from '../model/GetQueryableFieldsResponse';
import type { LogContextRequest, LogContextResponse } from '../model/LogContext';
import type { LogItem } from '../model/LogItem';
import type { LogPatternsRequest, LogPatternsResponse } from '../model/LogPattern';
import type { LogQueryRequest } from '../model/LogQueryRequest';
import type { LogQueryResponse } from '../model/LogQueryResponse';
import type { PatternChangesRequest, PatternChangesResponse } from '../model/PatternChanges';
import type { LogsStats } from '../model/ProjectLogStats';
import type { ExecuteSQLQueryRequest, SQLQueryResponse } from '../model/SQLQuery';
import type { SearchFrame } from '../model/SearchFrame';
import type { SystemDashboard } from '../model/SystemDashboard';

// The Site couldn't stream (older Site, expired token, a 4xx); fall back to executeQuery.
export class SearchStreamUnavailableError extends Error {}

// The stream went silent for a minute; cause is the abort it ended in.
export class SearchStalledError extends Error {
  constructor(readonly cause: unknown) {
    super(
      'The search stopped responding (no data for a minute) - try again, or narrow the time range',
    );
  }
}

export const queryApi = {
  async executeQuery(projectId: string, request: LogQueryRequest): Promise<LogQueryResponse> {
    const requestOptions: RequestOptions = new RequestOptions();
    requestOptions.setBody(JSON.stringify(request));
    return apiHelper.fetchPostJson(
      `${getApiBase()}/logs/query/execute/${projectId}`,
      requestOptions,
    );
  },

  // Rows arrive window by window (newest first) so the first ones show early;
  // resolves to the whole page, as executeQuery would.
  async streamQuery(
    projectId: string,
    request: LogQueryRequest,
    onRows: (logs: LogItem[]) => void,
    signal: AbortSignal,
  ): Promise<LogQueryResponse> {
    const logs: LogItem[] = [];
    try {
      return await streamNdjson<SearchFrame, LogQueryResponse>(
        `${getApiBase()}/logs/query/stream/${projectId}`,
        request,
        signal,
        (frame) => {
          if (frame.type === 'rows' && frame.logs?.length) {
            logs.push(...frame.logs);
            onRows(frame.logs);
          } else if (frame.type === 'done') {
            return {
              logs,
              total: frame.total ?? -1,
              limit: request.limit ?? logs.length,
              offset: request.offset ?? 0,
              executedIn: frame.executedIn ?? '',
              nextCursor: frame.nextCursor,
              countSkipped: frame.countSkipped,
              partial: frame.partial,
              searchedThrough: frame.searchedThrough,
            };
          }
          return undefined;
        },
      );
    } catch (error) {
      if (error instanceof StreamUnavailableError) {
        throw new SearchStreamUnavailableError(error.message);
      }
      if (error instanceof StreamStalledError) throw new SearchStalledError(error.cause);
      throw error;
    }
  },

  // Most frequent message templates among the matching logs.
  async getPatterns(
    projectId: string,
    request: LogPatternsRequest,
    signal?: AbortSignal,
  ): Promise<LogPatternsResponse> {
    const requestOptions: RequestOptions = new RequestOptions();
    requestOptions.setBody(JSON.stringify(request));
    if (signal) requestOptions.setSignal(signal);
    return apiHelper.fetchPostJson(
      `${getApiBase()}/logs/query/patterns/${projectId}`,
      requestOptions,
    );
  },

  // Patterns new, spiking or dropping against a baseline range.
  async getPatternChanges(
    projectId: string,
    request: PatternChangesRequest,
    signal?: AbortSignal,
  ): Promise<PatternChangesResponse> {
    const requestOptions: RequestOptions = new RequestOptions();
    requestOptions.setBody(JSON.stringify(request));
    if (signal) requestOptions.setSignal(signal);
    return apiHelper.fetchPostJson(
      `${getApiBase()}/logs/query/changes/${projectId}`,
      requestOptions,
    );
  },

  // Deployment rollouts in the range - the histogram's deploy markers.
  async getDeploys(
    projectId: string,
    request: DeploysRequest,
    signal?: AbortSignal,
  ): Promise<DeploysResponse> {
    const requestOptions: RequestOptions = new RequestOptions();
    requestOptions.setBody(JSON.stringify(request));
    if (signal) requestOptions.setSignal(signal);
    return apiHelper.fetchPostJson(
      `${getApiBase()}/logs/query/deploys/${projectId}`,
      requestOptions,
    );
  },

  // The logs just before and after one log, from its source.
  async getLogContext(
    projectId: string,
    request: LogContextRequest,
    signal?: AbortSignal,
  ): Promise<LogContextResponse> {
    const requestOptions: RequestOptions = new RequestOptions();
    requestOptions.setBody(JSON.stringify(request));
    if (signal) requestOptions.setSignal(signal);
    return apiHelper.fetchPostJson(
      `${getApiBase()}/logs/query/context/${projectId}`,
      requestOptions,
    );
  },

  // A field's most frequent values among the matching logs.
  async getFieldValues(
    projectId: string,
    request: FieldValuesRequest,
    signal?: AbortSignal,
  ): Promise<FieldValuesResponse> {
    const requestOptions: RequestOptions = new RequestOptions();
    requestOptions.setBody(JSON.stringify(request));
    if (signal) requestOptions.setSignal(signal);
    return apiHelper.fetchPostJson(
      `${getApiBase()}/logs/query/field-values/${projectId}`,
      requestOptions,
    );
  },

  async executeSQLQuery(
    projectId: string,
    request: ExecuteSQLQueryRequest,
  ): Promise<SQLQueryResponse> {
    const requestOptions: RequestOptions = new RequestOptions();
    requestOptions.setBody(JSON.stringify(request));
    return apiHelper.fetchPostJson(
      `${getApiBase()}/logs/query/execute-sql/${projectId}`,
      requestOptions,
    );
  },

  async getQueryableFields(
    projectId: string,
    request?: GetQueryableFieldsRequest,
  ): Promise<GetQueryableFieldsResponse> {
    const requestOptions: RequestOptions = new RequestOptions();

    let url = `${getApiBase()}/logs/query/fields/${projectId}`;
    if (request?.query) {
      const searchParams = new URLSearchParams({ query: request.query });
      url += `?${searchParams.toString()}`;
    }

    return apiHelper.fetchGetJson(url, requestOptions);
  },

  async getProjectStats(projectId: string): Promise<LogsStats> {
    const requestOptions: RequestOptions = new RequestOptions();
    return apiHelper.fetchGetJson(`${getApiBase()}/logs/query/stats/${projectId}`, requestOptions);
  },

  async getSystemStats(): Promise<LogsStats> {
    const requestOptions: RequestOptions = new RequestOptions();
    return apiHelper.fetchGetJson(`${getApiBase()}/logs/query/system-stats`, requestOptions);
  },

  async getSystemDashboard(): Promise<SystemDashboard> {
    const requestOptions: RequestOptions = new RequestOptions();
    return apiHelper.fetchGetJson(
      `${getApiBase()}/logs/query/system-dashboard`,
      requestOptions,
      true,
    );
  },
};
