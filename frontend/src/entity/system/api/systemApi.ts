import { getApiBase, getApplicationServer } from '../../../constants';
import RequestOptions from '../../../shared/api/RequestOptions';
import { apiHelper } from '../../../shared/api/apiHelper';
import type { SystemDashboard } from '../../query/model/SystemDashboard';
import type { LatestRelease } from '../model/LatestRelease';
import type { SystemUsage } from '../model/SystemUsage';
import type { UpgradeStatus } from '../model/UpgradeStatus';

/** 'site': the selected Site; 'central': this origin; `site:<id>`: that Site via the proxy. */
export type UpgradeStatusScope = 'site' | 'central' | `site:${string}`;

const base = (scope: UpgradeStatusScope) => {
  if (scope === 'central') return `${getApplicationServer()}/api/v1`;
  if (scope === 'site') return getApiBase();
  return `${getApplicationServer()}/sites/${encodeURIComponent(scope.slice('site:'.length))}/api/v1`;
};

export const systemApi = {
  async getUpgradeStatus(scope: UpgradeStatusScope): Promise<UpgradeStatus> {
    return apiHelper.fetchGetJson(`${base(scope)}/system/upgrade-status`, new RequestOptions());
  },
  /**
   * The newest Obzly release, which this origin's server asks GitHub for (cached 6 h); refresh
   * asks again if the answer is over a minute old. Never through a Site.
   */
  async getLatestRelease(refresh = false): Promise<LatestRelease> {
    return apiHelper.fetchGetJson(
      `${getApplicationServer()}/api/v1/system/latest-release${refresh ? '?refresh=true' : ''}`,
      new RequestOptions(),
    );
  },
  /** Hourly ingest over a day and data kept, by signal; Sites only. */
  async getUsage(scope: UpgradeStatusScope): Promise<SystemUsage> {
    return apiHelper.fetchGetJson(`${base(scope)}/system/usage`, new RequestOptions());
  },
  /** Host CPU, memory and disk and log totals; Sites only. */
  async getDashboard(scope: UpgradeStatusScope): Promise<SystemDashboard> {
    return apiHelper.fetchGetJson(
      `${base(scope)}/logs/query/system-dashboard`,
      new RequestOptions(),
    );
  },
};
