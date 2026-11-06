import { getApiBase } from '../../../constants';
import RequestOptions from '../../../shared/api/RequestOptions';
import { apiHelper } from '../../../shared/api/apiHelper';
import type { ApiKey } from '../model/ApiKey';
import type { BulkDeleteApiKeysRequest } from '../model/BulkDeleteApiKeysRequest';
import type { BulkDeleteApiKeysResponse } from '../model/BulkDeleteApiKeysResponse';
import type { CreateApiKeyRequest } from '../model/CreateApiKeyRequest';
import type { DeleteApiKeyRequest } from '../model/DeleteApiKeyRequest';
import type { GetApiKeysResponse } from '../model/GetApiKeysResponse';
import type { UpdateApiKeyRequest } from '../model/UpdateApiKeyRequest';
import type { VerifyApiKeyRequest } from '../model/VerifyApiKeyRequest';
import type { VerifyApiKeyResponse } from '../model/VerifyApiKeyResponse';

export const apiKeyApi = {
  async createApiKey(projectId: string, request: CreateApiKeyRequest): Promise<ApiKey> {
    const requestOptions: RequestOptions = new RequestOptions();
    requestOptions.setBody(JSON.stringify(request));
    return apiHelper.fetchPostJson(
      `${getApiBase()}/projects/api-keys/${projectId}`,
      requestOptions,
    );
  },

  async getApiKeys(projectId: string): Promise<GetApiKeysResponse> {
    const requestOptions: RequestOptions = new RequestOptions();
    return apiHelper.fetchGetJson(`${getApiBase()}/projects/api-keys/${projectId}`, requestOptions);
  },

  async updateApiKey(
    projectId: string,
    apiKeyId: string,
    request: UpdateApiKeyRequest,
  ): Promise<{ message: string }> {
    const requestOptions: RequestOptions = new RequestOptions();
    requestOptions.setBody(JSON.stringify(request));
    return apiHelper.fetchPutJson(
      `${getApiBase()}/projects/api-keys/${projectId}/${apiKeyId}`,
      requestOptions,
    );
  },

  async deleteApiKey(
    projectId: string,
    apiKeyId: string,
    request?: DeleteApiKeyRequest,
  ): Promise<{ message: string }> {
    const requestOptions: RequestOptions = new RequestOptions();
    if (request) {
      requestOptions.setBody(JSON.stringify(request));
    }
    return apiHelper.fetchDeleteJson(
      `${getApiBase()}/projects/api-keys/${projectId}/${apiKeyId}`,
      requestOptions,
    );
  },

  async bulkDeleteApiKeys(
    projectId: string,
    request: BulkDeleteApiKeysRequest,
  ): Promise<BulkDeleteApiKeysResponse> {
    const requestOptions: RequestOptions = new RequestOptions();
    requestOptions.setBody(JSON.stringify(request));
    return apiHelper.fetchPostJson(
      `${getApiBase()}/projects/api-keys/${projectId}/bulk-delete`,
      requestOptions,
    );
  },

  async verifyApiKey(
    projectId: string,
    request: VerifyApiKeyRequest,
  ): Promise<VerifyApiKeyResponse> {
    const requestOptions: RequestOptions = new RequestOptions();
    requestOptions.setBody(JSON.stringify(request));
    return apiHelper.fetchPostJson(
      `${getApiBase()}/projects/api-keys/${projectId}/verify`,
      requestOptions,
    );
  },
};
