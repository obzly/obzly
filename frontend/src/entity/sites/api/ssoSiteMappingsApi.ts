import { getApplicationServer } from '../../../constants';
import RequestOptions from '../../../shared/api/RequestOptions';
import { apiHelper } from '../../../shared/api/apiHelper';
import type {
  CreateSSOSiteGroupMappingRequest,
  SSOSiteGroupMapping,
} from '../model/SSOSiteGroupMapping';

const base = (connectorId: string) =>
  `${getApplicationServer()}/api/v1/control-plane/admin/sso/connectors/${connectorId}/site-group-mappings`;

export const ssoSiteMappingsApi = {
  async list(connectorId: string): Promise<SSOSiteGroupMapping[]> {
    const response: { mappings: SSOSiteGroupMapping[] } = await apiHelper.fetchGetJson(
      base(connectorId),
      new RequestOptions(),
    );
    return response.mappings;
  },

  async create(
    connectorId: string,
    request: CreateSSOSiteGroupMappingRequest,
  ): Promise<SSOSiteGroupMapping> {
    return apiHelper.fetchPostJson(
      base(connectorId),
      new RequestOptions().setBody(JSON.stringify(request)),
    );
  },

  async delete(connectorId: string, mappingId: string): Promise<unknown> {
    return apiHelper.fetchDeleteJson(`${base(connectorId)}/${mappingId}`, new RequestOptions());
  },
};
