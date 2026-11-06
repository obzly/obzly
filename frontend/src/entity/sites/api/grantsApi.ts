import { getApplicationServer } from '../../../constants';
import RequestOptions from '../../../shared/api/RequestOptions';
import { apiHelper } from '../../../shared/api/apiHelper';
import type { CreateGrantRequest, SiteGrant } from '../model/Grant';

const base = () => `${getApplicationServer()}/api/v1/control-plane`;

export const grantsApi = {
  async listForSite(siteId: string): Promise<SiteGrant[]> {
    return apiHelper.fetchGetJson(`${base()}/admin/sites/${siteId}/grants`, new RequestOptions());
  },

  async createGrant(request: CreateGrantRequest): Promise<SiteGrant> {
    const options = new RequestOptions().setBody(JSON.stringify(request));
    return apiHelper.fetchPostJson(`${base()}/admin/grants`, options);
  },

  async deleteGrant(id: string): Promise<{ success: boolean }> {
    return apiHelper.fetchDeleteJson(`${base()}/admin/grants/${id}`, new RequestOptions());
  },
};
