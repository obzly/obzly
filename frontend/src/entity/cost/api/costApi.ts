// Cost lives on each Site; a control plane reads every reachable Site, so calls take a Site ID
// ('' = this deployment) rather than the selected one, like inventory.
import { getApplicationServer } from '../../../constants';
import RequestOptions from '../../../shared/api/RequestOptions';
import { apiHelper } from '../../../shared/api/apiHelper';
import type {
  CommitmentsResponse,
  CostAlertSettings,
  CostAnomaly,
  CostBudget,
  CostBudgetRequest,
  CostRecommendation,
  CostSummary,
  ExploreRequest,
  ExploreResponse,
  FacetsResponse,
  PlannerResponse,
  RecommendationUpdate,
  RecommendationsResponse,
  ResourceCostResponse,
} from '../model/Cost';

const base = (siteId: string) =>
  siteId
    ? `${getApplicationServer()}/sites/${encodeURIComponent(siteId)}/api/v1`
    : `${getApplicationServer()}/api/v1`;

const post = (value: unknown, signal?: AbortSignal) =>
  new RequestOptions().setBody(JSON.stringify(value)).setSignal(signal);

export interface ProjectChannel {
  id: string;
  name: string;
  kind: string;
}

export const costApi = {
  summary: (siteId: string, signal?: AbortSignal): Promise<CostSummary> =>
    apiHelper.fetchGetJson(
      `${base(siteId)}/cloud-cost/summary`,
      new RequestOptions().setSignal(signal),
    ),

  explore: (
    siteId: string,
    request: ExploreRequest,
    signal?: AbortSignal,
  ): Promise<ExploreResponse> =>
    apiHelper.fetchPostJson(`${base(siteId)}/cloud-cost/explore`, post(request, signal)),

  facets: (
    siteId: string,
    request: ExploreRequest,
    signal?: AbortSignal,
  ): Promise<FacetsResponse> =>
    apiHelper.fetchPostJson(`${base(siteId)}/cloud-cost/facets`, post(request, signal)),

  resource: (
    siteId: string,
    key: string,
    days: number,
    signal?: AbortSignal,
  ): Promise<ResourceCostResponse> =>
    apiHelper.fetchGetJson(
      `${base(siteId)}/cloud-cost/resource?key=${encodeURIComponent(key)}&days=${days}`,
      new RequestOptions().setSignal(signal),
    ),

  anomalies: (
    siteId: string,
    from: string,
    to: string,
    signal?: AbortSignal,
  ): Promise<CostAnomaly[]> =>
    apiHelper.fetchGetJson(
      `${base(siteId)}/cloud-cost/anomalies?from=${from}&to=${to}`,
      new RequestOptions().setSignal(signal),
    ),

  updateAnomaly: (
    siteId: string,
    id: string,
    status: CostAnomaly['status'],
    note: string,
  ): Promise<CostAnomaly> =>
    apiHelper.fetchPatchJson(`${base(siteId)}/cloud-cost/anomalies/${id}`, post({ status, note })),

  budgets: (siteId: string, projectId: string, signal?: AbortSignal): Promise<CostBudget[]> =>
    apiHelper.fetchGetJson(
      `${base(siteId)}/projects/${projectId}/cost-budgets`,
      new RequestOptions().setSignal(signal),
    ),

  createBudget: (
    siteId: string,
    projectId: string,
    request: CostBudgetRequest,
  ): Promise<CostBudget> =>
    apiHelper.fetchPostJson(`${base(siteId)}/projects/${projectId}/cost-budgets`, post(request)),

  updateBudget: (
    siteId: string,
    projectId: string,
    id: string,
    request: CostBudgetRequest,
  ): Promise<CostBudget> =>
    apiHelper.fetchPutJson(
      `${base(siteId)}/projects/${projectId}/cost-budgets/${id}`,
      post(request),
    ),

  deleteBudget: (siteId: string, projectId: string, id: string): Promise<unknown> =>
    apiHelper.fetchDeleteJson(`${base(siteId)}/projects/${projectId}/cost-budgets/${id}`),

  alertSettings: (siteId: string, projectId: string): Promise<CostAlertSettings> =>
    apiHelper.fetchGetJson(`${base(siteId)}/projects/${projectId}/cost-alerts`),

  updateAlertSettings: (
    siteId: string,
    projectId: string,
    settings: CostAlertSettings,
  ): Promise<CostAlertSettings> =>
    apiHelper.fetchPutJson(`${base(siteId)}/projects/${projectId}/cost-alerts`, post(settings)),

  /** The project's notification channels on that Site, for budget and anomaly alerts. */
  channels: (siteId: string, projectId: string): Promise<ProjectChannel[]> =>
    apiHelper.fetchGetJson(`${base(siteId)}/projects/${projectId}/notification-channels`),

  recommendations: (siteId: string, signal?: AbortSignal): Promise<RecommendationsResponse> =>
    apiHelper.fetchGetJson(
      `${base(siteId)}/cloud-cost/recommendations`,
      new RequestOptions().setSignal(signal),
    ),

  updateRecommendation: (
    siteId: string,
    update: RecommendationUpdate,
  ): Promise<CostRecommendation> =>
    apiHelper.fetchPatchJson(`${base(siteId)}/cloud-cost/recommendations`, post(update)),

  resourceRecommendations: (
    siteId: string,
    key: string,
    signal?: AbortSignal,
  ): Promise<CostRecommendation[]> =>
    apiHelper.fetchGetJson(
      `${base(siteId)}/cloud-cost/resource-recommendations?key=${encodeURIComponent(key)}`,
      new RequestOptions().setSignal(signal),
    ),

  commitments: (siteId: string, signal?: AbortSignal): Promise<CommitmentsResponse> =>
    apiHelper.fetchGetJson(
      `${base(siteId)}/cloud-cost/commitments`,
      new RequestOptions().setSignal(signal),
    ),

  planner: (
    siteId: string,
    lookbackDays: number,
    rightsizingFirst: boolean,
    signal?: AbortSignal,
  ): Promise<PlannerResponse> =>
    apiHelper.fetchGetJson(
      `${base(siteId)}/cloud-cost/planner?lookbackDays=${lookbackDays}&rightsizingFirst=${rightsizingFirst}`,
      new RequestOptions().setSignal(signal),
    ),
};
