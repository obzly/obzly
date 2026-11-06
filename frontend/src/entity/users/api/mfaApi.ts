import { getApplicationServer } from '../../../constants';
import RequestOptions from '../../../shared/api/RequestOptions';
import { apiHelper } from '../../../shared/api/apiHelper';
import type { MFASetupResponse } from '../model/MFASetupResponse';
import type { MFAStatus } from '../model/MFAStatus';
import type { SignInResponse } from '../model/SignInResponse';
import { userApi } from './userApi';

export const mfaApi = {
  async getStatus(): Promise<MFAStatus> {
    const requestOptions: RequestOptions = new RequestOptions();
    return apiHelper.fetchGetJson(
      `${getApplicationServer()}/api/v1/users/mfa/status`,
      requestOptions,
    );
  },

  async setup(): Promise<MFASetupResponse> {
    const requestOptions: RequestOptions = new RequestOptions();
    return apiHelper.fetchPostJson(
      `${getApplicationServer()}/api/v1/users/mfa/setup`,
      requestOptions,
    );
  },

  async enable(code: string): Promise<{ message: string }> {
    const requestOptions: RequestOptions = new RequestOptions();
    requestOptions.setBody(JSON.stringify({ code }));
    return apiHelper.fetchPostJson(
      `${getApplicationServer()}/api/v1/users/mfa/enable`,
      requestOptions,
    );
  },

  async disable(code: string): Promise<{ message: string }> {
    const requestOptions: RequestOptions = new RequestOptions();
    requestOptions.setBody(JSON.stringify({ code }));
    return apiHelper.fetchPostJson(
      `${getApplicationServer()}/api/v1/users/mfa/disable`,
      requestOptions,
    );
  },

  // Sign-in time: authorized by the short-lived mfaToken, not a session.

  async loginSetup(mfaToken: string): Promise<MFASetupResponse> {
    const requestOptions: RequestOptions = new RequestOptions();
    requestOptions.setBody(JSON.stringify({ mfaToken }));
    return apiHelper.fetchPostJson(
      `${getApplicationServer()}/api/v1/users/mfa/login-setup`,
      requestOptions,
    );
  },

  async loginEnable(mfaToken: string, code: string): Promise<SignInResponse> {
    const requestOptions: RequestOptions = new RequestOptions();
    requestOptions.setBody(JSON.stringify({ mfaToken, code }));
    return apiHelper
      .fetchPostJson(`${getApplicationServer()}/api/v1/users/mfa/login-enable`, requestOptions)
      .then((response: unknown): SignInResponse => {
        const typedResponse = response as SignInResponse;
        if (typedResponse.token && typedResponse.userId) {
          userApi.saveAuthorizedData(typedResponse.token, typedResponse.userId);
          userApi.notifyAuthListeners();
        }
        return typedResponse;
      });
  },

  async loginVerify(mfaToken: string, code: string): Promise<SignInResponse> {
    const requestOptions: RequestOptions = new RequestOptions();
    requestOptions.setBody(JSON.stringify({ mfaToken, code }));
    return apiHelper
      .fetchPostJson(`${getApplicationServer()}/api/v1/users/mfa/login-verify`, requestOptions)
      .then((response: unknown): SignInResponse => {
        const typedResponse = response as SignInResponse;
        if (typedResponse.token && typedResponse.userId) {
          userApi.saveAuthorizedData(typedResponse.token, typedResponse.userId);
          userApi.notifyAuthListeners();
        }
        return typedResponse;
      });
  },

  async resetUserMFA(userId: string): Promise<{ message: string }> {
    const requestOptions: RequestOptions = new RequestOptions();
    return apiHelper.fetchPostJson(
      `${getApplicationServer()}/api/v1/users/${userId}/reset-mfa`,
      requestOptions,
    );
  },
};
