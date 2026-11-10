// Saved views belong to the user, so they live where the account does (getApplicationServer).
import { getApplicationServer } from '../../../constants';
import RequestOptions from '../../../shared/api/RequestOptions';
import { apiHelper } from '../../../shared/api/apiHelper';
import type { SavedView, SavedViewKind, SavedViewList, SavedViewRequest } from '../model/SavedView';

const base = () => `${getApplicationServer()}/api/v1/saved-views`;
const body = (value: unknown) => new RequestOptions().setBody(JSON.stringify(value));

export const savedViewsApi = {
  list: <State>(kind: SavedViewKind): Promise<SavedViewList<State>> =>
    apiHelper.fetchGetJson(`${base()}?kind=${kind}`),
  create: <State>(request: SavedViewRequest<State>): Promise<SavedView<State>> =>
    apiHelper.fetchPostJson(base(), body(request)),
  update: <State>(id: string, request: SavedViewRequest<State>): Promise<SavedView<State>> =>
    apiHelper.fetchPutJson(`${base()}/${id}`, body(request)),
  remove: (id: string): Promise<unknown> => apiHelper.fetchDeleteJson(`${base()}/${id}`),
  setDefault: <State>(kind: SavedViewKind, viewId: string | null): Promise<SavedViewList<State>> =>
    apiHelper.fetchPutJson(`${base()}/default`, body({ kind, viewId })),
};
