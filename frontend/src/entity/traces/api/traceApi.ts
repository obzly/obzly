import { getApiBase } from '../../../constants';
import RequestOptions from '../../../shared/api/RequestOptions';
import { apiHelper } from '../../../shared/api/apiHelper';
import type { SpansStats } from '../model/SpansStats';
import type {
  GetTraceResponse,
  ListOperationsResponse,
  ListServicesResponse,
  ListTracesRequest,
  ListTracesResponse,
} from '../model/Trace';

const baseUrl = () => `${getApiBase()}/traces/query`;

export const traceApi = {
  async listTraces(projectId: string, request: ListTracesRequest): Promise<ListTracesResponse> {
    const requestOptions = new RequestOptions().setBody(JSON.stringify(request));
    return apiHelper.fetchPostJson(`${baseUrl()}/list/${projectId}`, requestOptions);
  },

  async getTrace(
    projectId: string,
    traceId: string,
    from?: string,
    to?: string,
  ): Promise<GetTraceResponse> {
    const searchParams = new URLSearchParams();
    if (from) searchParams.set('from', from);
    if (to) searchParams.set('to', to);
    const query = searchParams.toString();

    return apiHelper.fetchGetJson(
      `${baseUrl()}/get/${projectId}/${traceId}${query ? `?${query}` : ''}`,
      new RequestOptions(),
    );
  },

  async listServices(projectId: string, from?: string, to?: string): Promise<ListServicesResponse> {
    const searchParams = new URLSearchParams();
    if (from) searchParams.set('from', from);
    if (to) searchParams.set('to', to);
    const query = searchParams.toString();

    return apiHelper.fetchGetJson(
      `${baseUrl()}/services/${projectId}${query ? `?${query}` : ''}`,
      new RequestOptions(),
    );
  },

  async listOperations(
    projectId: string,
    service: string,
    from?: string,
    to?: string,
  ): Promise<ListOperationsResponse> {
    const searchParams = new URLSearchParams({ service });
    if (from) searchParams.set('from', from);
    if (to) searchParams.set('to', to);

    return apiHelper.fetchGetJson(
      `${baseUrl()}/operations/${projectId}?${searchParams.toString()}`,
      new RequestOptions(),
    );
  },

  async getProjectStats(projectId: string): Promise<SpansStats> {
    return apiHelper.fetchGetJson(`${baseUrl()}/stats/${projectId}`, new RequestOptions());
  },
};
