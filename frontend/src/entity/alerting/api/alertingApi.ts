import { getApiBase } from '../../../constants';
import RequestOptions from '../../../shared/api/RequestOptions';
import { apiHelper } from '../../../shared/api/apiHelper';
import type {
  AlertRule,
  AlertView,
  NotificationChannel,
  PreviewSeries,
  RuleRequest,
  Silence,
  SilenceRequest,
} from '../model/Alerting';

const base = (projectId: string) => `${getApiBase()}/projects/${projectId}`;

const withBody = (body: unknown): RequestOptions => {
  const options = new RequestOptions();
  options.setBody(JSON.stringify(body));
  return options;
};

export const alertingApi = {
  listAlerts: (projectId: string): Promise<AlertView[]> =>
    apiHelper.fetchGetJson(`${base(projectId)}/alerts`, new RequestOptions()),

  listRules: (projectId: string): Promise<AlertRule[]> =>
    apiHelper.fetchGetJson(`${base(projectId)}/alert-rules`, new RequestOptions()),
  createRule: (projectId: string, rule: RuleRequest): Promise<AlertRule> =>
    apiHelper.fetchPostJson(`${base(projectId)}/alert-rules`, withBody(rule)),
  updateRule: (projectId: string, ruleId: string, rule: RuleRequest): Promise<AlertRule> =>
    apiHelper.fetchPutJson(`${base(projectId)}/alert-rules/${ruleId}`, withBody(rule)),
  deleteRule: (projectId: string, ruleId: string): Promise<{ message: string }> =>
    apiHelper.fetchDeleteJson(`${base(projectId)}/alert-rules/${ruleId}`, new RequestOptions()),
  previewRule: (projectId: string, rule: RuleRequest): Promise<PreviewSeries[]> =>
    apiHelper.fetchPostJson(`${base(projectId)}/alert-rules/preview`, withBody(rule)),

  listChannels: (projectId: string): Promise<NotificationChannel[]> =>
    apiHelper.fetchGetJson(`${base(projectId)}/notification-channels`, new RequestOptions()),
  testChannel: (projectId: string, channelId: string): Promise<{ message: string }> =>
    apiHelper.fetchPostJson(
      `${base(projectId)}/notification-channels/${channelId}/test`,
      new RequestOptions(),
    ),

  listSilences: (projectId: string): Promise<Silence[]> =>
    apiHelper.fetchGetJson(`${base(projectId)}/silences`, new RequestOptions()),
  createSilence: (projectId: string, silence: SilenceRequest): Promise<Silence> =>
    apiHelper.fetchPostJson(`${base(projectId)}/silences`, withBody(silence)),
  deleteSilence: (projectId: string, silenceId: string): Promise<{ message: string }> =>
    apiHelper.fetchDeleteJson(`${base(projectId)}/silences/${silenceId}`, new RequestOptions()),
};
