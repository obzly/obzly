import { getApiBase, getApplicationServer } from '../../../constants';
import RequestOptions from '../../../shared/api/RequestOptions';
import { apiHelper } from '../../../shared/api/apiHelper';
import type { SystemDashboard } from '../../query/model/SystemDashboard';
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
