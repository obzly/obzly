// A channel's message template: the organisation default for its kind, or
// its own (same editor), plus its "Use emoji" switch.
import { LoadingOutlined } from '@ant-design/icons';
import { Alert, App, Modal, Spin, Switch } from 'antd';
import { useEffect, useState } from 'react';

import {
  type MessageTemplate,
  type SettingsChannel,
  notificationSettingsApi,
} from '../../../entity/alerting';
import { templateFor } from '../lib/messageTemplates';
import { TemplateEditorComponent } from './TemplateEditorComponent';
import { errorMessage } from './lib';

interface Props {
  channel: SettingsChannel;
  /** Read-only: a Site enrolled in a control plane. */
  managed: boolean;
  onClose: () => void;
  onSaved: () => void;
}

export const ChannelTemplateModal = ({ channel, managed, onClose, onSaved }: Props) => {
  const { message } = App.useApp();
  const [useOrgDefault, setUseOrgDefault] = useState(!channel.templateOverride);
  const [useEmoji, setUseEmoji] = useState(channel.useEmoji);
  const [orgTemplate, setOrgTemplate] = useState<MessageTemplate>();
  const [draft, setDraft] = useState<MessageTemplate>({
    subject: channel.templateSubject,
    body: channel.templateBody,
  });
  const [isSaving, setIsSaving] = useState(false);

  useEffect(() => {
    const load = async () => {
      try {
        const view = await notificationSettingsApi.listTemplates();
        const item = view.templates.find((template) => template.kind === channel.kind);
        const org = { subject: item?.subject ?? '', body: item?.body ?? '' };
        setOrgTemplate(org);
        // Its own template starts from the organisation default.
        if (!channel.templateOverride) setDraft(org);
      } catch (error) {
        message.error(errorMessage(error, 'Failed to load the organisation template'));
        setOrgTemplate({ subject: '', body: '' });
      }
    };
    void load();
  }, [channel.id]);

  const save = async () => {
    setIsSaving(true);
    try {
      await notificationSettingsApi.saveChannelTemplate(channel.id, {
        useOrgDefault,
        useEmoji,
        ...(useOrgDefault ? { subject: '', body: '' } : templateFor(channel.kind, draft)),
      });
      message.success(`Saved ${channel.name}'s message settings`);
      onSaved();
    } catch (error) {
      message.error(errorMessage(error, 'Failed to save'));
    } finally {
      setIsSaving(false);
    }
  };

  return (
    <Modal
      open
      width={1100}
      title={`Message template: ${channel.name}`}
      onCancel={onClose}
      onOk={managed ? onClose : save}
      okText={managed ? 'Close' : 'Save'}
      confirmLoading={isSaving}
    >
      <div className="flex flex-col gap-4">
        {managed && (
          <Alert type="info" showIcon title="Managed on the control plane (read-only here)." />
        )}
        {channel.templateError && (
          <Alert
            type="warning"
            showIcon
            title="Template error at send time: the built-in template was sent instead"
            description={<span className="font-mono text-xs">{channel.templateError}</span>}
          />
        )}
        <div className="flex flex-wrap gap-6 text-sm text-gray-800 dark:text-gray-100">
          <label className="inline-flex items-center gap-2">
            <Switch checked={useOrgDefault} disabled={managed} onChange={setUseOrgDefault} />
            Use organisation default
          </label>
          <label className="inline-flex items-center gap-2">
            <Switch checked={useEmoji} disabled={managed} onChange={setUseEmoji} />
            Use emoji
          </label>
        </div>
        {!orgTemplate ? (
          <Spin indicator={<LoadingOutlined spin />} />
        ) : (
          <TemplateEditorComponent
            kind={channel.kind}
            value={useOrgDefault ? orgTemplate : draft}
            useEmoji={useEmoji}
            readOnly={managed || useOrgDefault}
            onChange={setDraft}
          />
        )}
      </div>
    </Modal>
  );
};
