// Deployment-level settings, so always this deployment's own origin.
import { getApplicationServer } from '../../../constants';
import RequestOptions from '../../../shared/api/RequestOptions';
import { apiHelper } from '../../../shared/api/apiHelper';
import type {
  AdoptResult,
  ChannelKind,
  ChannelTemplateRequest,
  MessageTemplate,
  MessageTemplatesView,
  SMTPRequest,
  SMTPSettings,
  SettingsChannel,
  SettingsChannelRequest,
  SettingsSync,
  TemplatePreviewRequest,
  TemplatePreviewResponse,
} from '../model/Alerting';

const base = () => `${getApplicationServer()}/api/v1/settings`;
const body = (value: unknown) => new RequestOptions().setBody(JSON.stringify(value));

export const notificationSettingsApi = {
  listChannels: (): Promise<SettingsChannel[]> => apiHelper.fetchGetJson(`${base()}/channels`),
  createChannel: (request: SettingsChannelRequest): Promise<SettingsChannel> =>
    apiHelper.fetchPostJson(`${base()}/channels`, body(request)),
  updateChannel: (id: string, request: SettingsChannelRequest): Promise<SettingsChannel> =>
    apiHelper.fetchPutJson(`${base()}/channels/${id}`, body(request)),
  deleteChannel: (id: string): Promise<unknown> =>
    apiHelper.fetchDeleteJson(`${base()}/channels/${id}`),
  testChannel: (id: string): Promise<{ message: string }> =>
    apiHelper.fetchPostJson(`${base()}/channels/${id}/test`),
  saveChannelTemplate: (id: string, request: ChannelTemplateRequest): Promise<SettingsChannel> =>
    apiHelper.fetchPutJson(`${base()}/channels/${id}/template`, body(request)),

  listTemplates: (): Promise<MessageTemplatesView> =>
    apiHelper.fetchGetJson(`${base()}/message-templates`),
  saveTemplate: (kind: ChannelKind, template: MessageTemplate): Promise<MessageTemplatesView> =>
    apiHelper.fetchPutJson(`${base()}/message-templates/${kind}`, body(template)),
  resetTemplate: (kind: ChannelKind): Promise<MessageTemplatesView> =>
    apiHelper.fetchDeleteJson(`${base()}/message-templates/${kind}`),
  previewTemplate: (request: TemplatePreviewRequest): Promise<TemplatePreviewResponse> =>
    apiHelper.fetchPostJson(`${base()}/message-templates/preview`, body(request)),
  /** Sends a test rendered with the (unsaved) template through a channel of that kind. */
  testTemplate: (request: TemplatePreviewRequest & { channelId: string }): Promise<unknown> =>
    apiHelper.fetchPostJson(`${base()}/message-templates/test`, body(request)),

  getSMTP: (): Promise<SMTPSettings> => apiHelper.fetchGetJson(`${base()}/smtp`),
  saveSMTP: (request: SMTPRequest): Promise<unknown> =>
    apiHelper.fetchPutJson(`${base()}/smtp`, body(request)),
  deleteSMTP: (): Promise<unknown> => apiHelper.fetchDeleteJson(`${base()}/smtp`),
  testSMTP: (to: string): Promise<{ message: string }> =>
    apiHelper.fetchPostJson(`${base()}/smtp/test`, body({ to })),

  getSync: (): Promise<SettingsSync> => apiHelper.fetchGetJson(`${base()}/sync`),
  /** fingerprint is the pending key's as shown, so a key that changed again isn't accepted unseen. */
  acceptSiteKey: (siteId: string, fingerprint: string): Promise<unknown> =>
    apiHelper.fetchPostJson(
      `${base()}/sync/${encodeURIComponent(siteId)}/accept-key`,
      body({ fingerprint }),
    ),

  /** Move to central: the Site seals its own settings to the control plane, which stores them. */
  async moveToCentral(siteId: string): Promise<AdoptResult> {
    const exported: { sealed: string } = await apiHelper.fetchGetJson(
      `${getApplicationServer()}/sites/${encodeURIComponent(siteId)}/api/v1/settings/local-config/export`,
    );
    return apiHelper.fetchPostJson(`${base()}/adopt`, body({ siteId, sealed: exported.sealed }));
  },
};
