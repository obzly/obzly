import { getApiBase } from '../../../constants';
import RequestOptions from '../../../shared/api/RequestOptions';
import { apiHelper } from '../../../shared/api/apiHelper';
import type {
  AiBudget,
  AiBudgetRequest,
  AiBudgetStatus,
  AiModelPrice,
  AiModelPriceRequest,
  AiModelPricesResponse,
  FillCommonPricesResponse,
} from '../model/Budgets';

const base = (projectId: string) => `${getApiBase()}/projects/${projectId}`;

const withBody = (body: unknown): RequestOptions => {
  const options = new RequestOptions();
  options.setBody(JSON.stringify(body));
  return options;
};

export const aiBudgetsApi = {
  listBudgets: (projectId: string): Promise<AiBudget[]> =>
    apiHelper.fetchGetJson(`${base(projectId)}/ai-budgets`, new RequestOptions()),
  getStatus: (projectId: string): Promise<AiBudgetStatus[]> =>
    apiHelper.fetchGetJson(`${base(projectId)}/ai-budgets/status`, new RequestOptions()),
  createBudget: (projectId: string, budget: AiBudgetRequest): Promise<AiBudget> =>
    apiHelper.fetchPostJson(`${base(projectId)}/ai-budgets`, withBody(budget)),
  updateBudget: (projectId: string, budgetId: string, budget: AiBudgetRequest): Promise<AiBudget> =>
    apiHelper.fetchPutJson(`${base(projectId)}/ai-budgets/${budgetId}`, withBody(budget)),
  deleteBudget: (projectId: string, budgetId: string): Promise<{ message: string }> =>
    apiHelper.fetchDeleteJson(`${base(projectId)}/ai-budgets/${budgetId}`, new RequestOptions()),

  listPrices: (projectId: string): Promise<AiModelPricesResponse> =>
    apiHelper.fetchGetJson(`${base(projectId)}/ai-model-prices`, new RequestOptions()),
  createPrice: (projectId: string, price: AiModelPriceRequest): Promise<AiModelPrice> =>
    apiHelper.fetchPostJson(`${base(projectId)}/ai-model-prices`, withBody(price)),
  updatePrice: (
    projectId: string,
    priceId: string,
    price: AiModelPriceRequest,
  ): Promise<AiModelPrice> =>
    apiHelper.fetchPutJson(`${base(projectId)}/ai-model-prices/${priceId}`, withBody(price)),
  deletePrice: (projectId: string, priceId: string): Promise<{ message: string }> =>
    apiHelper.fetchDeleteJson(
      `${base(projectId)}/ai-model-prices/${priceId}`,
      new RequestOptions(),
    ),
  fillCommonPrices: (projectId: string): Promise<FillCommonPricesResponse> =>
    apiHelper.fetchPostJson(`${base(projectId)}/ai-model-prices/fill-common`, new RequestOptions()),
};
