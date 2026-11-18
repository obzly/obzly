// Settings → General: this Site's identity and who can join and do what.
import { CopyOutlined, LoadingOutlined } from '@ant-design/icons';
import { App, Button, Spin, Switch, Tooltip } from 'antd';
import { useEffect, useState } from 'react';

import {
  CONTROL_PLANE_URL,
  IS_CLOUD,
  IS_CONTROL_PLANE,
  SITE_ID,
  SITE_NAME,
  WEBSITE_URL,
} from '../../../constants';
import { settingsApi } from '../../../entity/users/api/settingsApi';
import type { UsersSettings } from '../../../entity/users/model/UsersSettings';
import { copyToClipboard } from '../../../shared/lib';
import { Card } from '../../../shared/ui/v2';
import { AppearanceSettingsComponent } from './AppearanceSettingsComponent';
import { PlansSettingsComponent } from './PlansSettingsComponent';

export function GeneralSettingsComponent() {
  const { message } = App.useApp();
  const [settings, setSettings] = useState<UsersSettings | undefined>(undefined);
  const [isLoading, setIsLoading] = useState(true);
  const [isSaving, setIsSaving] = useState(false);
  const [hasChanges, setHasChanges] = useState(false);

  const [formSettings, setFormSettings] = useState<UsersSettings>({
    isAllowExternalRegistrations: false,
    isAllowMemberInvitations: false,
    isMemberAllowedToCreateProjects: false,
  });

  useEffect(() => {
    loadSettings();
  }, []);

  const loadSettings = async () => {
    setIsLoading(true);

    try {
      const currentSettings = await settingsApi.getSettings();
      setSettings(currentSettings);
      setFormSettings(currentSettings);
      setHasChanges(false);
    } catch (error: unknown) {
      const errorMessage = error instanceof Error ? error.message : 'Failed to load settings';
      message.error(errorMessage);
    } finally {
      setIsLoading(false);
    }
  };

  const handleSettingChange = (key: keyof UsersSettings, value: boolean) => {
    const newFormSettings = { ...formSettings, [key]: value };
    setFormSettings(newFormSettings);

    if (settings) {
      const hasAnyChanges = (
        [
          'isAllowExternalRegistrations',
          'isAllowMemberInvitations',
          'isMemberAllowedToCreateProjects',
        ] as const
      ).some((settingKey) => newFormSettings[settingKey] !== settings[settingKey]);
      setHasChanges(hasAnyChanges);
    }
  };

  const handleSave = async () => {
    if (!hasChanges) return;

    setIsSaving(true);
    try {
      // Identity & access saves other fields of the same settings, so merge onto the latest.
      const latest = await settingsApi.getSettings();
      const updatedSettings = await settingsApi.updateSettings({
        ...latest,
        isAllowExternalRegistrations: formSettings.isAllowExternalRegistrations,
        isAllowMemberInvitations: formSettings.isAllowMemberInvitations,
        isMemberAllowedToCreateProjects: formSettings.isMemberAllowedToCreateProjects,
      });
      setSettings(updatedSettings);
      setFormSettings(updatedSettings);
      setHasChanges(false);
      message.success('Settings updated successfully');
    } catch (error: unknown) {
      const errorMessage = error instanceof Error ? error.message : 'Failed to update settings';
      message.error(errorMessage);
    } finally {
      setIsSaving(false);
    }
  };

  const handleReset = () => {
    if (settings) {
      setFormSettings(settings);
      setHasChanges(false);
    }
  };

  const switchRow = (
    key:
      | 'isAllowExternalRegistrations'
      | 'isAllowMemberInvitations'
      | 'isMemberAllowedToCreateProjects',
    title: string,
    help: string,
  ) => (
    <div className="flex items-start justify-between gap-6 border-t border-gray-200 pt-3 first:border-t-0 first:pt-0 dark:border-gray-700">
      <div className="min-w-0 flex-1">
        <div className="font-medium text-gray-900 dark:text-gray-100">{title}</div>
        <div className="mt-1 text-gray-500 dark:text-gray-300">{help}</div>
      </div>
      <Switch
        aria-label={title}
        checked={formSettings[key]}
        onChange={(checked) => handleSettingChange(key, checked)}
      />
    </div>
  );

  return (
    <div className="flex flex-col gap-4">
      {/* Only on a Site enrolled with a control plane. */}
      {!IS_CONTROL_PLANE && SITE_ID && (
        <Card
          title="This Site"
          subtitle="Register this Site on the control plane under this exact ID. Set by this deployment's SITE_ID."
        >
          <dl className="m-0 grid grid-cols-[max-content_1fr] items-center gap-x-4 gap-y-1.5 text-sm">
            <dt className="text-gray-500 dark:text-gray-300">Site ID</dt>
            <dd className="m-0 flex items-center gap-1">
              <span className="font-mono">{SITE_ID}</span>
              <Tooltip title="Copy Site ID">
                <Button
                  type="text"
                  size="small"
                  icon={<CopyOutlined />}
                  aria-label="Copy Site ID"
                  onClick={async () => {
                    await copyToClipboard(SITE_ID);
                    message.success('Site ID copied');
                  }}
                />
              </Tooltip>
            </dd>
            {SITE_NAME && SITE_NAME !== SITE_ID && (
              <>
                <dt className="text-gray-500 dark:text-gray-300">Name</dt>
                <dd className="m-0">{SITE_NAME}</dd>
              </>
            )}
            {CONTROL_PLANE_URL && (
              <>
                <dt className="text-gray-500 dark:text-gray-300">Control plane</dt>
                <dd className="m-0 truncate">
                  <a
                    href={CONTROL_PLANE_URL}
                    target="_blank"
                    rel="noopener noreferrer"
                    className="text-emerald-600 hover:underline"
                  >
                    {CONTROL_PLANE_URL}
                  </a>
                </dd>
              </>
            )}
          </dl>
        </Card>
      )}

      <Card
        title="Access policy"
        subtitle="Who can join and what members may do. MFA and sign-in are under Identity & access."
        extra={
          <span className="text-sm text-gray-500 dark:text-gray-300">
            Read more about settings you can{' '}
            <a
              href={`${WEBSITE_URL}/settings`}
              target="_blank"
              rel="noreferrer"
              className="!text-emerald-600"
            >
              here
            </a>
          </span>
        }
      >
        {isLoading ? (
          <Spin indicator={<LoadingOutlined spin />} />
        ) : (
          <div className="flex max-w-2xl flex-col gap-3 text-sm">
            {switchRow(
              'isAllowExternalRegistrations',
              'Allow external registrations',
              'When enabled, anyone can register an account. When disabled, new users need an invitation - except SSO users in a group mapped to access (see Single Sign-On → Group mappings), who get an account on first sign-in.',
            )}
            {!formSettings.isAllowExternalRegistrations &&
              switchRow(
                'isAllowMemberInvitations',
                'Allow member invitations',
                'When enabled, existing members can invite new users to join Obzly. If not - only admins can invite users.',
              )}
            {switchRow(
              'isMemberAllowedToCreateProjects',
              'Members can create projects',
              'When enabled, members (non-admin users) can create new projects. If not - only admins can create projects.',
            )}

            {hasChanges && (
              <div className="flex items-center gap-2 border-t border-gray-200 pt-3 dark:border-gray-700">
                <span className="flex-1 text-xs text-gray-500 dark:text-gray-400">
                  Unsaved changes
                </span>
                <Button type="default" onClick={handleReset} disabled={isSaving}>
                  Reset
                </Button>
                <Button
                  type="primary"
                  onClick={handleSave}
                  loading={isSaving}
                  disabled={isSaving}
                  className="border-emerald-600 bg-emerald-600 hover:border-emerald-700 hover:bg-emerald-700"
                >
                  {isSaving ? 'Saving...' : 'Save Changes'}
                </Button>
              </div>
            )}
          </div>
        )}
      </Card>

      {settings && <AppearanceSettingsComponent initialSettings={settings} />}

      {IS_CLOUD && <PlansSettingsComponent />}
    </div>
  );
}
