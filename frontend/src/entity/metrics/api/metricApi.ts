import { getApiBase } from '../../../constants';
import RequestOptions from '../../../shared/api/RequestOptions';
import { apiHelper } from '../../../shared/api/apiHelper';
import type {
  ListLabelValuesRequest,
  ListLabelValuesResponse,
  ListMetricNamesResponse,
} from '../model/MetricName';
import type { MetricsStats } from '../model/MetricsStats';

const baseUrl = () => `${getApiBase()}/metrics/query`;

export const metricApi = {
  async listMetricNames(projectId: string): Promise<ListMetricNamesResponse> {
    return apiHelper.fetchGetJson(`${baseUrl()}/names/${projectId}`, new RequestOptions());
  },

  // A label's values across all metrics, all time by default, so filter
  // dropdowns offer values not active in the current window.
  async listLabelValues(
    projectId: string,
    request: ListLabelValuesRequest,
  ): Promise<ListLabelValuesResponse> {
    const requestOptions = new RequestOptions().setBody(JSON.stringify(request));
    return apiHelper.fetchPostJson(`${baseUrl()}/label-values/${projectId}`, requestOptions);
  },

  async getProjectStats(projectId: string): Promise<MetricsStats> {
    return apiHelper.fetchGetJson(`${baseUrl()}/stats/${projectId}`, new RequestOptions());
  },
};
