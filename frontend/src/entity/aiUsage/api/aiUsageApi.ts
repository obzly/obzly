import { getApiBase } from '../../../constants';
import RequestOptions from '../../../shared/api/RequestOptions';
import { apiHelper } from '../../../shared/api/apiHelper';
import type {
  AiUsageEventsParams,
  AiUsageEventsResponse,
  AiUsageOverview,
  AiUsageOverviewParams,
  AiUsageSessionEventsResponse,
  AiUsageSessionsParams,
  AiUsageSessionsResponse,
} from '../model/AiUsage';
import type { LlmCallsResponse, LlmOverview, LlmOverviewParams } from '../model/LlmApps';

const baseUrl = () => `${getApiBase()}/ai-usage`;

const toQuery = (params: object): string => {
  const search = new URLSearchParams();
  for (const [key, value] of Object.entries(params)) {
    if (value !== undefined && value !== null && value !== '') search.set(key, String(value));
  }
  return search.toString();
};

export const aiUsageApi = {
  async getOverview(projectId: string, params: AiUsageOverviewParams): Promise<AiUsageOverview> {
    return apiHelper.fetchGetJson(
      `${baseUrl()}/overview/${projectId}?${toQuery(params)}`,
      new RequestOptions(),
    );
  },

  async getSessions(
    projectId: string,
    params: AiUsageSessionsParams,
  ): Promise<AiUsageSessionsResponse> {
    return apiHelper.fetchGetJson(
      `${baseUrl()}/sessions/${projectId}?${toQuery(params)}`,
      new RequestOptions(),
    );
  },

  async getEvents(projectId: string, params: AiUsageEventsParams): Promise<AiUsageEventsResponse> {
    return apiHelper.fetchGetJson(
      `${baseUrl()}/events/${projectId}?${toQuery(params)}`,
      new RequestOptions(),
    );
  },

  async getLlmOverview(projectId: string, params: LlmOverviewParams): Promise<LlmOverview> {
    return apiHelper.fetchGetJson(
      `${baseUrl()}/llm/overview/${projectId}?${toQuery(params)}`,
      new RequestOptions(),
    );
  },

  async getLlmCalls(
    projectId: string,
    params: { from: string; to: string; limit?: number },
  ): Promise<LlmCallsResponse> {
    return apiHelper.fetchGetJson(
      `${baseUrl()}/llm/calls/${projectId}?${toQuery(params)}`,
      new RequestOptions(),
    );
  },

  async getSessionEvents(
    projectId: string,
    sessionId: string,
    range: { from: string; to: string },
  ): Promise<AiUsageSessionEventsResponse> {
    return apiHelper.fetchGetJson(
      `${baseUrl()}/sessions/${projectId}/${encodeURIComponent(sessionId)}/events?${toQuery(range)}`,
      new RequestOptions(),
    );
  },
};
