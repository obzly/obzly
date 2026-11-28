import { getApplicationServer } from '../../../constants';
import RequestOptions from '../../../shared/api/RequestOptions';
import { apiHelper } from '../../../shared/api/apiHelper';
import type { AccessToken, CreateAccessTokenResponse } from '../model/AccessToken';

// Identity routes: always the server's own origin, never a Site.
const baseUrl = () => `${getApplicationServer()}/api/v1/access-tokens`;

export const accessTokenApi = {
  /** writeAvailable is false while the server has MCP write access turned off. */
  async list(): Promise<{ accessTokens: AccessToken[]; writeAvailable: boolean }> {
    const response: { accessTokens: AccessToken[]; writeAvailable?: boolean } =
      await apiHelper.fetchGetJson(baseUrl(), new RequestOptions());
    return { accessTokens: response.accessTokens, writeAvailable: !!response.writeAvailable };
  },

  /** write adds the mcp:write scope (MCP write tools); it needs a session that passed MFA. */
  async create(
    name: string,
    expiresInDays: number,
    mfaCode?: string,
    write = false,
  ): Promise<CreateAccessTokenResponse> {
    const requestOptions = new RequestOptions();
    requestOptions.setBody(
      JSON.stringify({ name, expiresInDays, mfaCode: mfaCode || undefined, write }),
    );
    return apiHelper.fetchPostJson(baseUrl(), requestOptions);
  },

  async revoke(id: string): Promise<void> {
    await apiHelper.fetchDeleteJson(`${baseUrl()}/${id}`, new RequestOptions());
  },
};
