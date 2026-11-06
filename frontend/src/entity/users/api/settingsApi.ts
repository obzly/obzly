import { getApplicationServer } from '../../../constants';
import RequestOptions from '../../../shared/api/RequestOptions';
import { apiHelper } from '../../../shared/api/apiHelper';
import type { UsersSettings } from '../model/UsersSettings';

// Always this origin: the proxy denies a Site's /users/settings.
export const settingsApi = {
  async getSettings(): Promise<UsersSettings> {
    const requestOptions: RequestOptions = new RequestOptions();
    return apiHelper.fetchGetJson(
      `${getApplicationServer()}/api/v1/users/settings`,
      requestOptions,
    );
  },

  async updateSettings(settings: UsersSettings): Promise<UsersSettings> {
    const requestOptions: RequestOptions = new RequestOptions();
    requestOptions.setBody(JSON.stringify(settings));
    return apiHelper.fetchPutJson(
      `${getApplicationServer()}/api/v1/users/settings`,
      requestOptions,
    );
  },
};
