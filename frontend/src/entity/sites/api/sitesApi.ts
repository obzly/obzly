// The Site registry is central, so this always uses the control plane's own origin.
import { getApplicationServer } from '../../../constants';
import RequestOptions from '../../../shared/api/RequestOptions';
import { apiHelper } from '../../../shared/api/apiHelper';
import type {
  CreateSiteRequest,
  Site,
  SiteAdmin,
  SiteCheckResult,
  SiteStatus,
  UpdateSiteRequest,
} from '../model/Site';

const base = () => `${getApplicationServer()}/api/v1/control-plane`;

export const sitesApi = {
  /** Sites the caller has any access to - the frontend's Site picker. */
  async listSites(): Promise<Site[]> {
    return apiHelper.fetchGetJson(`${base()}/sites`, new RequestOptions());
  },

  /** Every registered Site, including disabled ones - admin only. */
  async listSitesAdmin(): Promise<SiteAdmin[]> {
    return apiHelper.fetchGetJson(`${base()}/admin/sites`, new RequestOptions());
  },

  async createSite(request: CreateSiteRequest): Promise<SiteAdmin> {
    const options = new RequestOptions().setBody(JSON.stringify(request));
    return apiHelper.fetchPostJson(`${base()}/admin/sites`, options);
  },

  async updateSite(id: string, request: UpdateSiteRequest): Promise<SiteAdmin> {
    const options = new RequestOptions().setBody(JSON.stringify(request));
    return apiHelper.fetchPutJson(`${base()}/admin/sites/${id}`, options);
  },

  async deleteSite(id: string): Promise<{ success: boolean }> {
    return apiHelper.fetchDeleteJson(`${base()}/admin/sites/${id}`, new RequestOptions());
  },

  async checkSite(id: string): Promise<SiteCheckResult> {
    return apiHelper.fetchPostJson(`${base()}/admin/sites/${id}/check`, new RequestOptions());
  },

  /** The Sites the caller can reach: ID and light only (no reason, version or latency). */
  async getReachableSiteStatuses(): Promise<Pick<SiteStatus, 'siteId' | 'status'>[]> {
    const response: { statuses: Pick<SiteStatus, 'siteId' | 'status'>[] } =
      await apiHelper.fetchGetJson(`${base()}/sites/status`, new RequestOptions());
    return response.statuses;
  },

  async getSiteStatuses(): Promise<SiteStatus[]> {
    const response: { statuses: SiteStatus[] } = await apiHelper.fetchGetJson(
      `${base()}/admin/sites/status`,
      new RequestOptions(),
    );
    return response.statuses;
  },
};
