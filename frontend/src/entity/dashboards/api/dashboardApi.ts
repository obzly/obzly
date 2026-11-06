import { getApiBase } from '../../../constants';
import RequestOptions from '../../../shared/api/RequestOptions';
import { apiHelper } from '../../../shared/api/apiHelper';
import { StreamUnavailableError, streamNdjson } from '../../../shared/api/ndjsonStream';
import type { AggregationBucket, AggregationResponse } from '../model/AggregationResponse';
import type { Dashboard } from '../model/Dashboard';
import type {
  CreateDashboardRequest,
  ImportDashboardRequest,
  ImportDashboardResponse,
  ListDashboardsResponse,
  ListFoldersResponse,
  PanelQueryRequest,
  UpdateDashboardRequest,
} from '../model/DashboardRequests';
import type { Folder } from '../model/Folder';

// One NDJSON line of a streamed panel query.
interface PanelFrame {
  type: 'buckets' | 'heartbeat' | 'done' | 'error';
  buckets?: AggregationBucket[];
  error?: string;
}

const baseUrl = (projectId: string) => `${getApiBase()}/projects/dashboards/${projectId}`;

export const dashboardApi = {
  async list(projectId: string): Promise<ListDashboardsResponse> {
    return apiHelper.fetchGetJson(baseUrl(projectId), new RequestOptions());
  },

  async get(projectId: string, dashboardId: string): Promise<Dashboard> {
    return apiHelper.fetchGetJson(`${baseUrl(projectId)}/${dashboardId}`, new RequestOptions());
  },

  async create(projectId: string, request: CreateDashboardRequest): Promise<Dashboard> {
    const requestOptions = new RequestOptions().setBody(JSON.stringify(request));
    return apiHelper.fetchPostJson(baseUrl(projectId), requestOptions);
  },

  async update(
    projectId: string,
    dashboardId: string,
    request: UpdateDashboardRequest,
  ): Promise<Dashboard> {
    const requestOptions = new RequestOptions().setBody(JSON.stringify(request));
    return apiHelper.fetchPutJson(`${baseUrl(projectId)}/${dashboardId}`, requestOptions);
  },

  async remove(projectId: string, dashboardId: string): Promise<void> {
    await apiHelper.fetchDeleteJson(`${baseUrl(projectId)}/${dashboardId}`, new RequestOptions());
  },

  async setPinned(projectId: string, dashboardId: string, pinned: boolean): Promise<Dashboard> {
    const requestOptions = new RequestOptions().setBody(JSON.stringify({ pinned }));
    return apiHelper.fetchPutJson(`${baseUrl(projectId)}/${dashboardId}/pin`, requestOptions);
  },

  async setFolder(
    projectId: string,
    dashboardId: string,
    folderId: string | null,
  ): Promise<Dashboard> {
    const requestOptions = new RequestOptions().setBody(JSON.stringify({ folderId }));
    return apiHelper.fetchPutJson(`${baseUrl(projectId)}/${dashboardId}/folder`, requestOptions);
  },

  async listFolders(projectId: string): Promise<ListFoldersResponse> {
    return apiHelper.fetchGetJson(`${baseUrl(projectId)}/folders`, new RequestOptions());
  },

  async createFolder(projectId: string, title: string): Promise<Folder> {
    const requestOptions = new RequestOptions().setBody(JSON.stringify({ title }));
    return apiHelper.fetchPostJson(`${baseUrl(projectId)}/folders`, requestOptions);
  },

  async renameFolder(projectId: string, folderId: string, title: string): Promise<Folder> {
    const requestOptions = new RequestOptions().setBody(JSON.stringify({ title }));
    return apiHelper.fetchPutJson(`${baseUrl(projectId)}/folders/${folderId}`, requestOptions);
  },

  async deleteFolder(projectId: string, folderId: string): Promise<void> {
    await apiHelper.fetchDeleteJson(
      `${baseUrl(projectId)}/folders/${folderId}`,
      new RequestOptions(),
    );
  },

  async executePanelQuery(
    projectId: string,
    request: PanelQueryRequest,
    signal?: AbortSignal,
  ): Promise<AggregationResponse> {
    const requestOptions = new RequestOptions().setBody(JSON.stringify(request)).setSignal(signal);
    return apiHelper.fetchPostJson(`${baseUrl(projectId)}/query`, requestOptions);
  },

  // Buckets arrive per partition group, newest first; onBuckets must add them
  // up since the same bucket can come again. Falls back to executePanelQuery.
  async streamPanelQuery(
    projectId: string,
    request: PanelQueryRequest,
    onBuckets: (buckets: AggregationBucket[]) => void,
    signal: AbortSignal,
  ): Promise<void> {
    try {
      await streamNdjson<PanelFrame, true>(
        `${baseUrl(projectId)}/query/stream`,
        request,
        signal,
        (frame) => {
          if (frame.type === 'buckets' && frame.buckets?.length) onBuckets(frame.buckets);
          return frame.type === 'done' ? true : undefined;
        },
      );
    } catch (error) {
      if (!(error instanceof StreamUnavailableError)) throw error;
      const response = await dashboardApi.executePanelQuery(projectId, request, signal);
      onBuckets(response.buckets);
    }
  },

  async generateOverview(projectId: string): Promise<Dashboard> {
    return apiHelper.fetchPostJson(`${baseUrl(projectId)}/overview`, new RequestOptions());
  },

  async importDashboard(
    projectId: string,
    request: ImportDashboardRequest,
  ): Promise<ImportDashboardResponse> {
    const requestOptions = new RequestOptions().setBody(JSON.stringify(request));
    return apiHelper.fetchPostJson(`${baseUrl(projectId)}/import`, requestOptions);
  },
};
