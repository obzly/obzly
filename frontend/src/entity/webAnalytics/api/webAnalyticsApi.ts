import { getApiBase } from '../../../constants';
import RequestOptions from '../../../shared/api/RequestOptions';
import { apiHelper } from '../../../shared/api/apiHelper';
import type {
  WebAnalyticsReport,
  WebAnalyticsReportRequest,
  WebAnalyticsSettings,
  WebAnalyticsSettingsUpdate,
} from '../model/WebAnalytics';

// Site data: through the control plane's /sites/{id} proxy in multi-site mode.
const base = (projectId: string) => `${getApiBase()}/projects/${projectId}/web-analytics`;

export const webAnalyticsApi = {
  report: (
    projectId: string,
    request: WebAnalyticsReportRequest,
    signal?: AbortSignal,
  ): Promise<WebAnalyticsReport> => {
    const query = new URLSearchParams({ from: request.from });
    if (request.to) query.set('to', request.to);
    if (request.sources?.length) query.set('sources', request.sources.join(','));
    if (request.host) query.set('host', request.host);
    return apiHelper.fetchGetJson(
      `${base(projectId)}/report?${query.toString()}`,
      new RequestOptions().setSignal(signal),
    );
  },

  settings: (projectId: string): Promise<WebAnalyticsSettings> =>
    apiHelper.fetchGetJson(`${base(projectId)}/settings`, new RequestOptions()),

  updateSettings: (
    projectId: string,
    update: WebAnalyticsSettingsUpdate,
  ): Promise<WebAnalyticsSettings> =>
    apiHelper.fetchPutJson(
      `${base(projectId)}/settings`,
      new RequestOptions().setBody(JSON.stringify(update)),
    ),
};
