// Posture lives on each Site; a control plane reads every reachable Site, so calls take a Site ID
// ('' = this deployment), like cost.
import { getApplicationServer } from '../../../constants';
import RequestOptions from '../../../shared/api/RequestOptions';
import { apiHelper } from '../../../shared/api/apiHelper';
import type {
  ActionUpdate,
  ActionsResponse,
  CoverageRow,
  Finding,
  FindingDetail,
  FindingUpdate,
  FindingsRequest,
  FindingsResponse,
  PostureAction,
  PostureAlertSettings,
  PostureFacets,
  PostureFilters,
  PostureSummary,
  TrendsRequest,
  TrendsResponse,
} from '../model/Posture';

const base = (siteId: string) =>
  siteId
    ? `${getApplicationServer()}/sites/${encodeURIComponent(siteId)}/api/v1`
    : `${getApplicationServer()}/api/v1`;

const post = (value: unknown, signal?: AbortSignal) =>
  new RequestOptions().setBody(JSON.stringify(value)).setSignal(signal);

export const postureApi = {
  summary: (siteId: string, signal?: AbortSignal): Promise<PostureSummary> =>
    apiHelper.fetchGetJson(
      `${base(siteId)}/posture/summary`,
      new RequestOptions().setSignal(signal),
    ),

  findings: (
    siteId: string,
    request: FindingsRequest,
    signal?: AbortSignal,
  ): Promise<FindingsResponse> =>
    apiHelper.fetchPostJson(`${base(siteId)}/posture/findings`, post(request, signal)),

  finding: (siteId: string, id: string, signal?: AbortSignal): Promise<FindingDetail> =>
    apiHelper.fetchGetJson(
      `${base(siteId)}/posture/findings/${id}`,
      new RequestOptions().setSignal(signal),
    ),

  updateFinding: (siteId: string, id: string, update: FindingUpdate): Promise<Finding> =>
    apiHelper.fetchPatchJson(`${base(siteId)}/posture/findings/${id}`, post(update)),

  facets: (siteId: string, filters: PostureFilters, signal?: AbortSignal): Promise<PostureFacets> =>
    apiHelper.fetchPostJson(`${base(siteId)}/posture/facets`, post(filters, signal)),

  trends: (siteId: string, request: TrendsRequest, signal?: AbortSignal): Promise<TrendsResponse> =>
    apiHelper.fetchPostJson(`${base(siteId)}/posture/trends`, post(request, signal)),

  coverage: (siteId: string, signal?: AbortSignal): Promise<CoverageRow[]> =>
    apiHelper.fetchGetJson(
      `${base(siteId)}/posture/coverage`,
      new RequestOptions().setSignal(signal),
    ),

  resource: (siteId: string, key: string, signal?: AbortSignal): Promise<Finding[]> =>
    apiHelper.fetchGetJson(
      `${base(siteId)}/posture/resource?key=${encodeURIComponent(key)}`,
      new RequestOptions().setSignal(signal),
    ),

  alertSettings: (siteId: string, projectId: string): Promise<PostureAlertSettings> =>
    apiHelper.fetchGetJson(`${base(siteId)}/projects/${projectId}/posture-alerts`),

  updateAlertSettings: (
    siteId: string,
    projectId: string,
    settings: PostureAlertSettings,
  ): Promise<PostureAlertSettings> =>
    apiHelper.fetchPutJson(`${base(siteId)}/projects/${projectId}/posture-alerts`, post(settings)),

  actions: (siteId: string, signal?: AbortSignal): Promise<ActionsResponse> =>
    apiHelper.fetchGetJson(
      `${base(siteId)}/posture/actions`,
      new RequestOptions().setSignal(signal),
    ),

  updateAction: (siteId: string, update: ActionUpdate): Promise<PostureAction> =>
    apiHelper.fetchPatchJson(`${base(siteId)}/posture/actions`, post(update)),
};
