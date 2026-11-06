import { getApplicationServer } from '../../../constants';
import RequestOptions from '../../../shared/api/RequestOptions';
import { apiHelper } from '../../../shared/api/apiHelper';
import type { CreateSSOConnectorRequest } from '../model/CreateSSOConnectorRequest';
import type { CreateSSOGroupMappingRequest } from '../model/CreateSSOGroupMappingRequest';
import type { OAuthCallbackRequest } from '../model/OAuthCallbackRequest';
import type { OAuthCallbackResponse } from '../model/OAuthCallbackResponse';
import type { PublicSSOConnector } from '../model/PublicSSOConnector';
import type { SSOConnector } from '../model/SSOConnector';
import type { SSOGroupMapping } from '../model/SSOGroupMapping';
import type { UpdateSSOConnectorRequest } from '../model/UpdateSSOConnectorRequest';
import { userApi } from './userApi';

export const ssoApi = {
  async listConnectors(): Promise<SSOConnector[]> {
    const requestOptions: RequestOptions = new RequestOptions();
    return apiHelper
      .fetchGetJson(`${getApplicationServer()}/api/v1/sso/connectors`, requestOptions)
      .then((response: unknown) => (response as { connectors: SSOConnector[] }).connectors);
  },

  async createConnector(request: CreateSSOConnectorRequest): Promise<SSOConnector> {
    const requestOptions: RequestOptions = new RequestOptions();
    requestOptions.setBody(JSON.stringify(request));
    return apiHelper.fetchPostJson(
      `${getApplicationServer()}/api/v1/sso/connectors`,
      requestOptions,
    );
  },

  async updateConnector(
    connectorId: string,
    request: UpdateSSOConnectorRequest,
  ): Promise<SSOConnector> {
    const requestOptions: RequestOptions = new RequestOptions();
    requestOptions.setBody(JSON.stringify(request));
    return apiHelper.fetchPutJson(
      `${getApplicationServer()}/api/v1/sso/connectors/${connectorId}`,
      requestOptions,
    );
  },

  /** mfaCode is required when the caller has MFA enabled. */
  async deleteConnector(connectorId: string, mfaCode?: string): Promise<{ message: string }> {
    const requestOptions: RequestOptions = new RequestOptions();
    if (mfaCode) {
      requestOptions.setBody(JSON.stringify({ mfaCode }));
    }
    return apiHelper.fetchDeleteJson(
      `${getApplicationServer()}/api/v1/sso/connectors/${connectorId}`,
      requestOptions,
    );
  },

  async listGroupMappings(connectorId: string): Promise<SSOGroupMapping[]> {
    const requestOptions: RequestOptions = new RequestOptions();
    return apiHelper
      .fetchGetJson(
        `${getApplicationServer()}/api/v1/sso/connectors/${connectorId}/group-mappings`,
        requestOptions,
      )
      .then((response: unknown) => (response as { mappings: SSOGroupMapping[] }).mappings);
  },

  async createGroupMapping(
    connectorId: string,
    request: CreateSSOGroupMappingRequest,
  ): Promise<SSOGroupMapping> {
    const requestOptions: RequestOptions = new RequestOptions();
    requestOptions.setBody(JSON.stringify(request));
    return apiHelper.fetchPostJson(
      `${getApplicationServer()}/api/v1/sso/connectors/${connectorId}/group-mappings`,
      requestOptions,
    );
  },

  async deleteGroupMapping(connectorId: string, mappingId: string): Promise<{ message: string }> {
    const requestOptions: RequestOptions = new RequestOptions();
    return apiHelper.fetchDeleteJson(
      `${getApplicationServer()}/api/v1/sso/connectors/${connectorId}/group-mappings/${mappingId}`,
      requestOptions,
    );
  },

  // Unauthenticated, for the login page.
  async listPublicConnectors(redirectUri: string): Promise<PublicSSOConnector[]> {
    const requestOptions: RequestOptions = new RequestOptions();
    const url = `${getApplicationServer()}/api/v1/auth/sso/connectors?redirectUri=${encodeURIComponent(
      redirectUri,
    )}`;

    return apiHelper
      .fetchGetJson(url, requestOptions, true)
      .then((response: unknown) => (response as { connectors: PublicSSOConnector[] }).connectors);
  },

  async handleCallback(
    request: OAuthCallbackRequest & { state: string },
  ): Promise<OAuthCallbackResponse> {
    const requestOptions: RequestOptions = new RequestOptions();
    requestOptions.setBody(JSON.stringify(request));

    return apiHelper
      .fetchPostJson(`${getApplicationServer()}/api/v1/auth/sso/callback`, requestOptions)
      .then((response: unknown): OAuthCallbackResponse => {
        const typedResponse = response as OAuthCallbackResponse;
        if (typedResponse.token && typedResponse.userId) {
          userApi.saveAuthorizedData(typedResponse.token, typedResponse.userId);
          userApi.notifyAuthListeners();
        }
        return typedResponse;
      });
  },
};
