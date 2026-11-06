// Takes a Site ID ('' = this deployment) rather than the selected Site, since
// a control plane reads every reachable Site.
import { getApplicationServer } from '../../../constants';
import RequestOptions from '../../../shared/api/RequestOptions';
import { apiHelper } from '../../../shared/api/apiHelper';
import type { PromQLRequest, PromQLResponse } from '../../synthetics';
import type { AssetChange, AssetDetail, AssetList, InventorySummary } from '../model/Inventory';

const base = (siteId: string) =>
  siteId
    ? `${getApplicationServer()}/sites/${encodeURIComponent(siteId)}/api/v1`
    : `${getApplicationServer()}/api/v1`;

const withSignal = (signal?: AbortSignal) => new RequestOptions().setSignal(signal);

export const inventoryApi = {
  summary: (siteId: string, signal?: AbortSignal): Promise<InventorySummary> =>
    apiHelper.fetchGetJson(`${base(siteId)}/inventory/summary`, withSignal(signal)),

  /** Up to 5000 assets, worst health first. */
  assets: (siteId: string, signal?: AbortSignal): Promise<AssetList> =>
    apiHelper.fetchGetJson(`${base(siteId)}/inventory/assets?limit=5000`, withSignal(signal)),

  asset: (siteId: string, id: string, signal?: AbortSignal): Promise<AssetDetail> =>
    apiHelper.fetchGetJson(`${base(siteId)}/inventory/assets/${id}`, withSignal(signal)),

  changes: (siteId: string, hours: number, signal?: AbortSignal): Promise<AssetChange[]> =>
    apiHelper.fetchGetJson(
      `${base(siteId)}/inventory/changes?hours=${hours}&limit=200`,
      withSignal(signal),
    ),

  promql: (
    siteId: string,
    projectId: string,
    request: PromQLRequest,
    signal?: AbortSignal,
  ): Promise<PromQLResponse> =>
    apiHelper.fetchPostJson(
      `${base(siteId)}/metrics/query/promql/${projectId}`,
      new RequestOptions().setBody(JSON.stringify(request)).setSignal(signal),
    ),
};
