import { getApiBase } from '../../../constants';
import RequestOptions from '../../../shared/api/RequestOptions';
import { apiHelper } from '../../../shared/api/apiHelper';
import type { ServiceMapResponse } from '../model/ServiceMap';

const baseUrl = () => `${getApiBase()}/service-map`;

export const serviceMapApi = {
  async getServiceMap(projectId: string, from: string, to: string): Promise<ServiceMapResponse> {
    const searchParams = new URLSearchParams({ from, to });

    return apiHelper.fetchGetJson(
      `${baseUrl()}/${projectId}?${searchParams.toString()}`,
      new RequestOptions(),
    );
  },
};
