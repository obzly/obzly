// Cloud connections live on the control plane, like Synthetics.
import { getApplicationServer } from '../../../constants';
import RequestOptions from '../../../shared/api/RequestOptions';
import { apiHelper } from '../../../shared/api/apiHelper';
import type {
  CloudConnection,
  CloudConnectionRequest,
  CloudSetup,
  CloudTest,
} from '../model/CloudConnection';

const base = () => `${getApplicationServer()}/api/v1/cloud-connections`;
const body = (value: unknown) => new RequestOptions().setBody(JSON.stringify(value));

export const cloudConnectionsApi = {
  list: (): Promise<CloudConnection[]> => apiHelper.fetchGetJson(base()),
  setup: (): Promise<CloudSetup> => apiHelper.fetchGetJson(`${base()}/setup`),
  create: (request: CloudConnectionRequest): Promise<CloudConnection> =>
    apiHelper.fetchPostJson(base(), body(request)),
  update: (id: string, request: CloudConnectionRequest): Promise<CloudConnection> =>
    apiHelper.fetchPutJson(`${base()}/${id}`, body(request)),
  remove: (id: string): Promise<unknown> => apiHelper.fetchDeleteJson(`${base()}/${id}`),
  test: (request: CloudConnectionRequest): Promise<CloudTest> =>
    apiHelper.fetchPostJson(`${base()}/test`, body(request)),
  getTest: (id: string): Promise<CloudTest> => apiHelper.fetchGetJson(`${base()}/tests/${id}`),
};
