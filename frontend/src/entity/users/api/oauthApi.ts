import { getApplicationServer } from '../../../constants';
import RequestOptions from '../../../shared/api/RequestOptions';
import { accessTokenHelper } from '../../../shared/api/accessTokenHelper';
import { apiHelper } from '../../../shared/api/apiHelper';
import type {
  OAuthConnectedApp,
  OAuthConsentInfo,
  OAuthValidateResult,
} from '../model/OAuthConsent';

const baseUrl = () => `${getApplicationServer()}/api/v1/oauth`;

// Raw fetch: error responses carry fields (redirectTo, fatal) the shared
// helper would drop.
const postConsent = async (path: string, body: Record<string, unknown>) => {
  const response = await fetch(`${baseUrl()}${path}`, {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
      Authorization: accessTokenHelper.getAccessToken() ?? '',
    },
    body: JSON.stringify(body),
  });
  const json = (await response.json().catch(() => ({}))) as Record<string, unknown>;
  return { ok: response.ok, json };
};

export const oauthApi = {
  async validate(params: Record<string, string>): Promise<OAuthValidateResult> {
    const { ok, json } = await postConsent('/authorize/validate', params);
    if (ok) {
      return { ok: true, info: json as unknown as OAuthConsentInfo };
    }
    return {
      ok: false,
      error: String(json.error ?? 'This authorization request is not valid'),
      redirectTo: json.fatal ? undefined : (json.redirectTo as string | undefined),
    };
  },

  async decide(params: Record<string, string>, approve: boolean): Promise<string> {
    const { ok, json } = await postConsent('/authorize/decision', { ...params, approve });
    if (!ok || typeof json.redirectTo !== 'string') {
      throw new Error(String(json.error ?? 'Authorization failed'));
    }
    return json.redirectTo;
  },

  async listConnectedApps(): Promise<OAuthConnectedApp[]> {
    const response: { apps: OAuthConnectedApp[] } = await apiHelper.fetchGetJson(
      `${baseUrl()}/connected-apps`,
      new RequestOptions(),
    );
    return response.apps;
  },

  async revokeConnectedApp(id: string): Promise<void> {
    await apiHelper.fetchDeleteJson(`${baseUrl()}/connected-apps/${id}`, new RequestOptions());
  },
};
