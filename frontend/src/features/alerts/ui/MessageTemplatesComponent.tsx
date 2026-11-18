// Obzly settings › Notifications & alerting › Message templates: the
// organisation default per channel kind (central on a control plane and sent
// to its Sites; a standalone Site's own).
import { LoadingOutlined, SendOutlined } from '@ant-design/icons';
import { Alert, App, Button, Popconfirm, Select, Spin, Switch } from 'antd';
import { useCallback, useEffect, useState } from 'react';

import { IS_CONTROL_PLANE } from '../../../constants';
import {
  type ChannelKind,
  type MessageTemplate,
  type MessageTemplatesView,
  type SettingsChannel,
  notificationSettingsApi,
} from '../../../entity/alerting';
import { Card } from '../../../shared/ui/v2';
import { TEMPLATE_KINDS, isTemplateDirty, templateFor } from '../lib/messageTemplates';
import { TemplateEditorComponent } from './TemplateEditorComponent';
import { PillTabs } from './bits';
import { errorMessage } from './lib';

interface Props {
  channels: SettingsChannel[];
}

export const MessageTemplatesComponent = ({ channels }: Props) => {
  const { message } = App.useApp();
  const [view, setView] = useState<MessageTemplatesView>();
  const [kind, setKind] = useState<ChannelKind>('email');
  const [drafts, setDrafts] = useState<Partial<Record<ChannelKind, MessageTemplate>>>({});
  const [useEmoji, setUseEmoji] = useState(true);
  const [testChannelId, setTestChannelId] = useState<string>();
  const [busy, setBusy] = useState<string | null>(null);
  const [loadError, setLoadError] = useState<string>();

  const apply = (next: MessageTemplatesView) => {
    setView(next);
    setDrafts(
      Object.fromEntries(
        next.templates.map((item) => [item.kind, { subject: item.subject, body: item.body }]),
      ),
    );
  };

  const load = useCallback(async () => {
    try {
      apply(await notificationSettingsApi.listTemplates());
    } catch (error) {
      setLoadError(errorMessage(error, 'Failed to load the message templates'));
    }
  }, []);

  useEffect(() => {
    void load();
  }, [load]);

  const stored = view?.templates.find((item) => item.kind === kind);
  const draft = drafts[kind] ?? { subject: '', body: '' };
  const managed = !!view?.managed;
  const dirty = !!stored && isTemplateDirty(stored, draft);
  const kindChannels = channels.filter((channel) => channel.kind === kind);

  const run = async (key: string, action: () => Promise<void>, success: string) => {
    setBusy(key);
    try {
      await action();
      message.success(success);
    } catch (error) {
      message.error(errorMessage(error, 'That didn’t work'));
    } finally {
      setBusy(null);
    }
  };

  return (
    <Card
      title="Message templates"
      subtitle={
        <>
          What each channel kind sends, for every rule, monitor and incident. A channel can use its
          own template instead (its Message template action).
          {IS_CONTROL_PLANE && ' Sites receive these with the channels within 5 minutes.'}
        </>
      }
      bodyClassName="flex flex-col gap-4"
    >
      {loadError && <Alert type="error" showIcon title={loadError} />}
      {!view && !loadError && <Spin indicator={<LoadingOutlined spin />} />}
      {view && (
        <>
          <PillTabs
            label="Channel kind"
            items={TEMPLATE_KINDS.map((item) => ({ key: item.value, label: item.label }))}
            active={kind}
            onChange={(next) => {
              setKind(next);
              setTestChannelId(undefined);
            }}
          />
          {managed && (
            <Alert
              type="info"
              showIcon
              title="Managed on the control plane: this Site uses the templates it sends."
            />
          )}
          {stored?.lastError && (
            <Alert
              type="warning"
              showIcon
              title="Template error at send time: the built-in template was sent instead"
              description={
                <span className="font-mono text-xs">
                  {stored.lastError}
                  {stored.lastErrorAt && ` (${new Date(stored.lastErrorAt).toLocaleString()})`}
                </span>
              }
            />
          )}
          <div className="flex flex-wrap items-center gap-3 text-xs text-gray-500 dark:text-gray-400">
            <span>{stored?.isDefault ? 'Built-in template' : 'Customised'}</span>
            <label className="inline-flex items-center gap-2">
              <Switch size="small" checked={useEmoji} onChange={setUseEmoji} />
              Preview with emoji
            </label>
          </div>
          <TemplateEditorComponent
            kind={kind}
            value={draft}
            useEmoji={useEmoji}
            readOnly={managed}
            onChange={(next) => setDrafts((current) => ({ ...current, [kind]: next }))}
          />
          <div className="flex flex-wrap items-center gap-2 border-t border-gray-200 pt-4 dark:border-gray-700">
            {!managed && (
              <>
                <Button
                  type="primary"
                  disabled={!dirty}
                  loading={busy === 'save'}
                  onClick={() =>
                    run(
                      'save',
                      async () =>
                        apply(
                          await notificationSettingsApi.saveTemplate(
                            kind,
                            templateFor(kind, draft),
                          ),
                        ),
                      'Template saved',
                    )
                  }
                >
                  Save
                </Button>
                {dirty && stored && (
                  <Button
                    onClick={() =>
                      setDrafts((current) => ({
                        ...current,
                        [kind]: { subject: stored.subject, body: stored.body },
                      }))
                    }
                  >
                    Discard changes
                  </Button>
                )}
                <Popconfirm
                  title="Go back to the built-in template for this kind?"
                  okText="Reset"
                  disabled={stored?.isDefault}
                  onConfirm={() =>
                    run(
                      'reset',
                      async () => apply(await notificationSettingsApi.resetTemplate(kind)),
                      'Reset to the built-in template',
                    )
                  }
                >
                  <Button disabled={stored?.isDefault} loading={busy === 'reset'}>
                    Reset to default
                  </Button>
                </Popconfirm>
              </>
            )}
            <span className="grow" />
            <Select
              className="min-w-[200px]"
              placeholder={
                kindChannels.length ? 'Send test to…' : `No ${kind} channels to test with`
              }
              disabled={kindChannels.length === 0}
              value={testChannelId}
              aria-label="Channel to send a test to"
              options={kindChannels.map((channel) => ({ value: channel.id, label: channel.name }))}
              onChange={setTestChannelId}
            />
            <Button
              icon={<SendOutlined />}
              disabled={!testChannelId}
              loading={busy === 'test'}
              onClick={() =>
                testChannelId &&
                run(
                  'test',
                  async () => {
                    await notificationSettingsApi.testTemplate({
                      kind,
                      ...templateFor(kind, draft),
                      useEmoji,
                      channelId: testChannelId,
                    });
                  },
                  'Test notification sent with this template',
                )
              }
            >
              Send test
            </Button>
          </div>
        </>
      )}
    </Card>
  );
};
