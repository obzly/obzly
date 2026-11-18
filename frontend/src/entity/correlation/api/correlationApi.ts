import { getApiBase } from '../../../constants';
import RequestOptions from '../../../shared/api/RequestOptions';
import { apiHelper } from '../../../shared/api/apiHelper';
import type { ListSessionsResponse } from '../model/Session';
import type { SessionDetail } from '../model/SessionDetail';
import type { SessionImpact } from '../model/SessionImpact';

const baseUrl = () => `${getApiBase()}/correlation/sessions`;

export const correlationApi = {
  async listSessions(
    projectId: string,
    from: string,
    to: string,
    limit?: number,
  ): Promise<ListSessionsResponse> {
    const searchParams = new URLSearchParams({ from, to });
    if (limit !== undefined) searchParams.set('limit', String(limit));

    return apiHelper.fetchGetJson(
      `${baseUrl()}/${projectId}?${searchParams.toString()}`,
      new RequestOptions(),
    );
  },

  async getSessionDetail(projectId: string, sessionId: string): Promise<SessionDetail> {
    return apiHelper.fetchGetJson(`${baseUrl()}/${projectId}/${sessionId}`, new RequestOptions());
  },

  async getSessionImpact(projectId: string, sessionId: string): Promise<SessionImpact> {
    return apiHelper.fetchGetJson(
      `${baseUrl()}/${projectId}/${sessionId}/impact`,
      new RequestOptions(),
    );
  },
};
