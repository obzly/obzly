// Config lives on the control plane; results are read through the proxy of
// each monitor's destination Site, not the Site picked in the UI.
import { getApplicationServer } from '../../../constants';
import RequestOptions from '../../../shared/api/RequestOptions';
import { apiHelper } from '../../../shared/api/apiHelper';
import type { LogItem } from '../../query/model/LogItem';
import type { LogQueryResponse } from '../../query/model/LogQueryResponse';
import type {
  CheckRequest,
  CheckView,
  Destination,
  DestinationRequest,
  ImportPreview,
  Probe,
  ProbeTokenResponse,
  PromQLRequest,
  PromQLResponse,
  RunResult,
  TestRequest,
  TestRun,
} from '../model/Synthetics';

const base = () => `${getApplicationServer()}/api/v1/synthetics`;
const siteBase = (siteId: string) =>
  `${getApplicationServer()}/sites/${encodeURIComponent(siteId)}/api/v1`;

const body = (value: unknown) => new RequestOptions().setBody(JSON.stringify(value));

export const syntheticsApi = {
  listChecks: (): Promise<CheckView[]> => apiHelper.fetchGetJson(`${base()}/checks`),
  getCheck: (id: string): Promise<CheckView> => apiHelper.fetchGetJson(`${base()}/checks/${id}`),
  createCheck: (request: CheckRequest): Promise<CheckView> =>
    apiHelper.fetchPostJson(`${base()}/checks`, body(request)),
  updateCheck: (id: string, request: CheckRequest): Promise<CheckView> =>
    apiHelper.fetchPutJson(`${base()}/checks/${id}`, body(request)),
  deleteCheck: (id: string): Promise<unknown> =>
    apiHelper.fetchDeleteJson(`${base()}/checks/${id}`),
  setEnabled: (id: string, enabled: boolean, version: number): Promise<CheckView> =>
    apiHelper.fetchPostJson(`${base()}/checks/${id}/enabled`, body({ enabled, version })),
  cloneCheck: (id: string): Promise<CheckView> =>
    apiHelper.fetchPostJson(`${base()}/checks/${id}/clone`),
  previewImport: (yaml: string, secrets: Record<string, string>): Promise<ImportPreview> =>
    apiHelper.fetchPostJson(`${base()}/checks/import/preview`, body({ yaml, secrets })),
  applyImport: (yaml: string, secrets: Record<string, string>): Promise<ImportPreview> =>
    apiHelper.fetchPostJson(`${base()}/checks/import`, body({ yaml, secrets })),
  exportChecks: (ids?: string[]): Promise<string> =>
    apiHelper.fetchGetRaw(`${base()}/checks/export${ids?.length ? `?ids=${ids.join(',')}` : ''}`),

  listDestinations: (): Promise<Destination[]> => apiHelper.fetchGetJson(`${base()}/destinations`),
  createDestination: (request: DestinationRequest): Promise<Destination> =>
    apiHelper.fetchPostJson(`${base()}/destinations`, body(request)),
  updateDestination: (id: string, request: DestinationRequest): Promise<Destination> =>
    apiHelper.fetchPutJson(`${base()}/destinations/${id}`, body(request)),
  deleteDestination: (id: string): Promise<unknown> =>
    apiHelper.fetchDeleteJson(`${base()}/destinations/${id}`),

  listProbes: (): Promise<Probe[]> => apiHelper.fetchGetJson(`${base()}/probes`),
  createProbe: (name: string, location: string, description: string): Promise<ProbeTokenResponse> =>
    apiHelper.fetchPostJson(`${base()}/probes`, body({ name, location, description })),
  /** revokeOld stops the old token at once instead of after 24 h. */
  rotateProbeToken: (id: string, revokeOld = false): Promise<ProbeTokenResponse> =>
    apiHelper.fetchPostJson(`${base()}/probes/${id}/token${revokeOld ? '?revokeOld=true' : ''}`),
  deleteProbe: (id: string): Promise<unknown> =>
    apiHelper.fetchDeleteJson(`${base()}/probes/${id}`),

  createTest: (request: TestRequest): Promise<TestRun> =>
    apiHelper.fetchPostJson(`${base()}/tests`, body(request)),
  getTest: (id: string): Promise<TestRun> => apiHelper.fetchGetJson(`${base()}/tests/${id}`),

  /** Mints an ingest key on a destination's Site (not the Site picked in the UI). */
  async createSiteApiKey(siteId: string, projectId: string, name: string): Promise<string> {
    const key: { token?: string } = await apiHelper.fetchPostJson(
      `${siteBase(siteId)}/projects/api-keys/${projectId}`,
      body({ name }),
    );
    if (!key.token) throw new Error('The Site returned no key');
    return key.token;
  },

  /** A PromQL query against a monitor's destination project. */
  promql: (
    siteId: string,
    projectId: string,
    request: PromQLRequest,
    signal?: AbortSignal,
  ): Promise<PromQLResponse> =>
    apiHelper.fetchPostJson(
      `${siteBase(siteId)}/metrics/query/promql/${projectId}`,
      body(request).setSignal(signal),
    ),

  /**
   * Run log lines of one check (newest first), each with its full result.
   * failuresOnly keeps the runs that weren't green (logged at warn or error).
   */
  async runs(
    siteId: string,
    projectId: string,
    checkId: string,
    from: Date,
    to: Date,
    limit: number,
    signal?: AbortSignal,
    failuresOnly = false,
  ): Promise<RunResult[]> {
    const response: LogQueryResponse = await apiHelper.fetchPostJson(
      `${siteBase(siteId)}/logs/query/execute/${projectId}`,
      body({
        query: {
          type: 'logical',
          logic: {
            operator: 'and',
            children: [
              {
                type: 'condition',
                condition: { field: 'service_name', operator: 'equals', value: 'obzly-probe' },
              },
              {
                type: 'condition',
                condition: { field: 'check_id', operator: 'equals', value: checkId },
              },
              ...(failuresOnly
                ? [
                    {
                      type: 'condition',
                      condition: { field: 'level', operator: 'not_equals', value: 'info' },
                    },
                  ]
                : []),
            ],
          },
        },
        timeRange: { from: from.toISOString(), to: to.toISOString() },
        limit,
        sortOrder: 'desc',
        trackTotal: false,
      }).setSignal(signal),
    );
    return response.logs.map(runFromLog).filter((run): run is RunResult => run !== null);
  },
};

const runFromLog = (log: LogItem): RunResult | null => {
  const encoded = log.fields?.synthetics_run;
  if (!encoded) return null;
  try {
    return JSON.parse(encoded) as RunResult;
  } catch {
    return null;
  }
};
