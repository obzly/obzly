import { LoadingOutlined } from '@ant-design/icons';
import { Alert, App, Checkbox, InputNumber, Modal, Select, Spin, Switch } from 'antd';
import { useEffect, useState } from 'react';

import {
  type IncidentNotificationRequest,
  type NotificationChannel,
  type Severity,
  alertingApi,
} from '../../../entity/alerting';
import { SEVERITIES } from '../../alerts/lib/messageTemplates';

interface Props {
  projectId: string;
  canManage: boolean;
  onClose: () => void;
}

const DEFAULTS: IncidentNotificationRequest = {
  notifyOpen: false,
  notifyEnd: false,
  minSignals: 3,
  minServices: 2,
  severity: 'high',
  channelIds: [],
};

/** Incidents › gear: notify the project's channels when an incident opens (and ends). */
export const IncidentNotificationsModal = ({ projectId, canManage, onClose }: Props) => {
  const { message } = App.useApp();
  const [draft, setDraft] = useState<IncidentNotificationRequest>(DEFAULTS);
  const [channels, setChannels] = useState<NotificationChannel[]>([]);
  const [isLoading, setIsLoading] = useState(true);
  const [isSaving, setIsSaving] = useState(false);
  const [loadError, setLoadError] = useState<string>();

  useEffect(() => {
    const load = async () => {
      try {
        const [settings, available] = await Promise.all([
          alertingApi.getIncidentNotifications(projectId),
          alertingApi.listChannels(projectId),
        ]);
        setDraft({
          notifyOpen: settings.notifyOpen,
          notifyEnd: settings.notifyEnd,
          minSignals: settings.minSignals,
          minServices: settings.minServices,
          severity: settings.severity,
          channelIds: settings.channelIds ?? [],
        });
        setChannels(available);
      } catch (error) {
        setLoadError(error instanceof Error ? error.message : 'Failed to load the settings');
      } finally {
        setIsLoading(false);
      }
    };
    void load();
  }, [projectId]);

  const set = (changes: Partial<IncidentNotificationRequest>) =>
    setDraft((current) => ({ ...current, ...changes }));

  const save = async () => {
    setIsSaving(true);
    try {
      await alertingApi.saveIncidentNotifications(projectId, draft);
      message.success('Incident notifications saved');
      onClose();
    } catch (error) {
      message.error(error instanceof Error ? error.message : 'Failed to save');
    } finally {
      setIsSaving(false);
    }
  };

  const enabled = draft.notifyOpen || draft.notifyEnd;
  const disabled = !canManage;

  return (
    <Modal
      open
      title="Incident notifications"
      onCancel={onClose}
      onOk={canManage ? save : onClose}
      okText={canManage ? 'Save' : 'Close'}
      confirmLoading={isSaving}
      okButtonProps={{
        disabled: !!loadError || (canManage && enabled && draft.channelIds.length === 0),
      }}
      cancelButtonProps={{ style: canManage ? undefined : { display: 'none' } }}
    >
      {isLoading ? (
        <div className="flex justify-center py-6">
          <Spin indicator={<LoadingOutlined spin />} />
        </div>
      ) : loadError ? (
        <Alert type="error" showIcon title={loadError} />
      ) : (
        <div className="flex flex-col gap-4">
          {!canManage && (
            <Alert type="info" showIcon title="Project owners and admins change these settings." />
          )}
          <label className="flex items-center gap-3 text-sm text-gray-800 dark:text-gray-100">
            <Switch
              checked={draft.notifyOpen}
              disabled={disabled}
              onChange={(notifyOpen) => set({ notifyOpen })}
            />
            Notify when an incident opens
          </label>
          <Checkbox
            checked={draft.notifyEnd}
            disabled={disabled}
            onChange={(event) => set({ notifyEnd: event.target.checked })}
          >
            Also notify when it ends (no new errors in its services for 15 minutes)
          </Checkbox>

          <div className="flex flex-col gap-1">
            <span className="text-[13px] font-medium text-gray-700 dark:text-gray-200">
              When an incident has at least
            </span>
            <div className="flex flex-wrap items-center gap-2 text-sm text-gray-700 dark:text-gray-200">
              <InputNumber
                min={1}
                max={100000}
                value={draft.minSignals}
                disabled={disabled}
                aria-label="Minimum error signals"
                onChange={(value) => set({ minSignals: value ?? 1 })}
              />
              error signals, or
              <InputNumber
                min={1}
                max={1000}
                value={draft.minServices}
                disabled={disabled}
                aria-label="Minimum services"
                onChange={(value) => set({ minServices: value ?? 1 })}
              />
              services
            </div>
            <span className="text-xs text-gray-500 dark:text-gray-400">
              Sessions that keep arriving for the same services count as one incident, notified
              once.
            </span>
          </div>

          <div className="flex flex-col gap-1">
            <span className="text-[13px] font-medium text-gray-700 dark:text-gray-200">
              Severity
            </span>
            <Select<Severity>
              className="w-40"
              value={draft.severity}
              disabled={disabled}
              options={SEVERITIES}
              onChange={(severity) => set({ severity })}
            />
          </div>

          <div className="flex flex-col gap-1">
            <span className="text-[13px] font-medium text-gray-700 dark:text-gray-200">Notify</span>
            <Select
              mode="multiple"
              value={draft.channelIds}
              disabled={disabled}
              placeholder={
                channels.length === 0 ? 'No channels are available to this project' : 'Channels'
              }
              options={channels.map((channel) => ({
                value: channel.id,
                label: `${channel.name} (${channel.kind})`,
              }))}
              onChange={(channelIds: string[]) => set({ channelIds })}
            />
            <span className="text-xs text-gray-500 dark:text-gray-400">
              Messages use each channel&apos;s template. A silence matching alertname=Incident mutes
              them.
            </span>
          </div>
        </div>
      )}
    </Modal>
  );
};
