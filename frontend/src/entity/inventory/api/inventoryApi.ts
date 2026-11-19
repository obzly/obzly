// Takes a Site ID ('' = this deployment) rather than the selected Site, since
// a control plane reads every reachable Site.
import { getApplicationServer } from '../../../constants';
import RequestOptions from '../../../shared/api/RequestOptions';
import { apiHelper } from '../../../shared/api/apiHelper';
import type { PromQLRequest, PromQLResponse } from '../../synthetics';
import type {
  AssetChange,
  AssetDetail,
  AssetList,
  AssetQuery,
  InventorySummary,
} from '../model/Inventory';
import { assetSearchParams } from './assetSearchParams';

const base = (siteId: string) =>
  siteId
    ? `${getApplicationServer()}/sites/${encodeURIComponent(siteId)}/api/v1`
    : `${getApplicationServer()}/api/v1`;

const withSignal = (signal?: AbortSignal) => new RequestOptions().setSignal(signal);

export const inventoryApi = {
  /** history adds the last 7 days' hourly counts by kind and those of 24 h ago. */
  summary: (siteId: string, signal?: AbortSignal, history = false): Promise<InventorySummary> =>
    apiHelper.fetchGetJson(
      `${base(siteId)}/inventory/summary${history ? '?history=true' : ''}`,
      withSignal(signal),
    ),

  /** A page of the matching assets (worst health first unless sorted) and how many matched. */
  assets: (siteId: string, query: AssetQuery, signal?: AbortSignal): Promise<AssetList> =>
    apiHelper.fetchGetJson(
      `${base(siteId)}/inventory/assets?${assetSearchParams(query).toString()}`,
      withSignal(signal),
    ),

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
