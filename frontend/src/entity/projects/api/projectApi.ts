import { getApiBase, getApplicationServer } from '../../../constants';
import RequestOptions from '../../../shared/api/RequestOptions';
import { apiHelper } from '../../../shared/api/apiHelper';
import type { GetAuditLogsRequest } from '../../audit-logs/model/GetAuditLogsRequest';
import type { GetAuditLogsResponse } from '../../audit-logs/model/GetAuditLogsResponse';
import type { CreateProjectRequest } from '../model/CreateProjectRequest';
import type { ListProjectsResponse } from '../model/ListProjectsResponse';
import type { Project } from '../model/Project';
import type { ProjectResponse } from '../model/ProjectResponse';
import type { PromotedField, PromotedFieldType } from '../model/PromotedField';
import type { RetentionPolicy } from '../model/RetentionPolicy';

export const projectApi = {
  async createProject(request: CreateProjectRequest): Promise<ProjectResponse> {
    const requestOptions: RequestOptions = new RequestOptions();
    requestOptions.setBody(JSON.stringify(request));
    return apiHelper.fetchPostJson(`${getApiBase()}/projects`, requestOptions);
  },

  async getProjects(): Promise<ListProjectsResponse> {
    const requestOptions: RequestOptions = new RequestOptions();
    return apiHelper.fetchGetJson(`${getApiBase()}/projects`, requestOptions);
  },

  /** Projects on a given (not the selected) Site; needs a grant there, else 403. */
  async getProjectsOnSite(siteId: string): Promise<ListProjectsResponse> {
    const requestOptions: RequestOptions = new RequestOptions();
    return apiHelper.fetchGetJson(
      `${getApplicationServer()}/sites/${encodeURIComponent(siteId)}/api/v1/projects`,
      requestOptions,
    );
  },

  async getProject(projectId: string): Promise<Project> {
    const requestOptions: RequestOptions = new RequestOptions();
    return apiHelper.fetchGetJson(`${getApiBase()}/projects/${projectId}`, requestOptions);
  },

  async updateProject(projectId: string, project: Project): Promise<Project> {
    const requestOptions: RequestOptions = new RequestOptions();
    requestOptions.setBody(JSON.stringify(project));
    return apiHelper.fetchPutJson(`${getApiBase()}/projects/${projectId}`, requestOptions);
  },

  async getRetentionPolicies(projectId: string): Promise<RetentionPolicy[]> {
    const requestOptions: RequestOptions = new RequestOptions();
    return apiHelper.fetchGetJson(
      `${getApiBase()}/projects/${projectId}/retention-policies`,
      requestOptions,
    );
  },

  async setRetentionPolicies(
    projectId: string,
    policies: RetentionPolicy[],
  ): Promise<RetentionPolicy[]> {
    const requestOptions: RequestOptions = new RequestOptions();
    requestOptions.setBody(JSON.stringify(policies));
    return apiHelper.fetchPutJson(
      `${getApiBase()}/projects/${projectId}/retention-policies`,
      requestOptions,
    );
  },

  async getPromotedFields(projectId: string): Promise<PromotedField[]> {
    const requestOptions: RequestOptions = new RequestOptions();
    return apiHelper.fetchGetJson(
      `${getApiBase()}/projects/${projectId}/promoted-fields`,
      requestOptions,
    );
  },

  async promoteField(
    projectId: string,
    field: string,
    type: PromotedFieldType,
  ): Promise<PromotedField> {
    const requestOptions: RequestOptions = new RequestOptions();
    requestOptions.setBody(JSON.stringify({ field, type }));
    return apiHelper.fetchPostJson(
      `${getApiBase()}/projects/${projectId}/promoted-fields`,
      requestOptions,
    );
  },

  async retirePromotedField(projectId: string, field: string): Promise<{ message: string }> {
    const requestOptions: RequestOptions = new RequestOptions();
    return apiHelper.fetchDeleteJson(
      `${getApiBase()}/projects/${projectId}/promoted-fields/${encodeURIComponent(field)}`,
      requestOptions,
    );
  },

  async deleteProject(projectId: string): Promise<{ message: string }> {
    const requestOptions: RequestOptions = new RequestOptions();
    return apiHelper.fetchDeleteJson(`${getApiBase()}/projects/${projectId}`, requestOptions);
  },

  async getProjectAuditLogs(
    projectId: string,
    params?: GetAuditLogsRequest,
  ): Promise<GetAuditLogsResponse> {
    const requestOptions: RequestOptions = new RequestOptions();

    let url = `${getApiBase()}/projects/${projectId}/audit-logs`;
    const urlParams = new URLSearchParams();

    if (params?.limit) {
      urlParams.append('limit', params.limit.toString());
    }
    if (params?.offset) {
      urlParams.append('offset', params.offset.toString());
    }
    if (params?.beforeDate) {
      urlParams.append('beforeDate', params.beforeDate);
    }
    for (const key of ['user', 'action', 'sortBy', 'sortOrder'] as const) {
      const value = params?.[key]?.trim();
      if (value) urlParams.append(key, value);
    }

    if (urlParams.toString()) {
      url += `?${urlParams.toString()}`;
    }

    return apiHelper.fetchGetJson(url, requestOptions);
  },
};
