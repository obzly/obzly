import { getApplicationServer } from '../../../constants';
import RequestOptions from '../../../shared/api/RequestOptions';
import { apiHelper } from '../../../shared/api/apiHelper';
import type { AccessToken, CreateAccessTokenResponse } from '../model/AccessToken';

// Identity routes: always the server's own origin, never a Site.
const baseUrl = () => `${getApplicationServer()}/api/v1/access-tokens`;

export const accessTokenApi = {
  async list(): Promise<AccessToken[]> {
    const response: { accessTokens: AccessToken[] } = await apiHelper.fetchGetJson(
      baseUrl(),
      new RequestOptions(),
    );
    return response.accessTokens;
  },

  async create(
    name: string,
    expiresInDays: number,
    mfaCode?: string,
  ): Promise<CreateAccessTokenResponse> {
    const requestOptions = new RequestOptions();
    requestOptions.setBody(JSON.stringify({ name, expiresInDays, mfaCode: mfaCode || undefined }));
    return apiHelper.fetchPostJson(baseUrl(), requestOptions);
  },

  async revoke(id: string): Promise<void> {
    await apiHelper.fetchDeleteJson(`${baseUrl()}/${id}`, new RequestOptions());
  },
};
