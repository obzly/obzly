import { LoadingOutlined } from '@ant-design/icons';
import { App, Spin, Switch, Tooltip } from 'antd';
import { useEffect, useState } from 'react';

import { settingsApi } from '../../../entity/users/api/settingsApi';
import { userManagementApi } from '../../../entity/users/api/userManagementApi';
import type { UserProfile } from '../../../entity/users/model/UserProfile';
import { UserRole } from '../../../entity/users/model/UserRole';
import type { UsersSettings } from '../../../entity/users/model/UsersSettings';
import { SSOSettingsComponent } from './SSOSettingsComponent';

// Only feeds counts here; the Users page paginates properly.
const USERS_LIMIT = 1000;

export const IdentityAccessComponent = () => {
  const { message } = App.useApp();
  const [settings, setSettings] = useState<UsersSettings>();
  const [users, setUsers] = useState<UserProfile[]>();
  const [saving, setSaving] = useState(false);

  useEffect(() => {
    settingsApi
      .getSettings()
      .then(setSettings)
      .catch((error: Error) => message.error(error.message));
    userManagementApi
      .getUsers({ limit: USERS_LIMIT })
      .then((response) => setUsers(response.users))
      .catch(() => setUsers([]));
  }, []);

  const setEveryone = async (required: boolean) => {
    setSaving(true);
    try {
      // General saves other fields of the same settings, so merge onto the latest.
      const latest = await settingsApi.getSettings();
      setSettings(
        await settingsApi.updateSettings({ ...latest, isMfaRequiredForEveryone: required }),
      );
      message.success(
        required
          ? 'MFA required for everyone: users without it set it up at their next sign-in'
          : 'MFA required for admins only',
      );
    } catch (error) {
      message.error((error as Error).message);
    } finally {
      setSaving(false);
    }
  };

  const active = (users ?? []).filter((user) => user.isActive);
  const admins = active.filter((user) => user.role === UserRole.ADMIN);
  const adminsWithMFA = admins.filter((user) => user.mfaEnabled).length;
  const withoutMFA = active.filter((user) => !user.mfaEnabled).length;

  return (
    <div className="flex flex-col gap-8">
      <section className="max-w-2xl">
        <h2 className="mb-1 text-xl font-bold">Multi-factor authentication</h2>
        <div className="mb-4 text-sm text-gray-500 dark:text-gray-300">
          TOTP authenticator apps. Sign-ins through an SSO connector set to trust its
          provider&apos;s MFA don&apos;t ask again.
        </div>
        {!settings ? (
          <Spin indicator={<LoadingOutlined spin />} />
        ) : (
          <div className="divide-y divide-gray-200 rounded-md border border-gray-200 text-sm dark:divide-gray-700 dark:border-gray-700">
            <div className="flex items-start justify-between gap-6 p-4">
              <div>
                <div className="font-medium text-gray-900 dark:text-gray-100">
                  Require MFA for admins
                </div>
                <div className="mt-1 text-gray-500 dark:text-gray-300">
                  {users
                    ? `${adminsWithMFA} of ${admins.length} admin${admins.length === 1 ? '' : 's'} ${adminsWithMFA === 1 ? 'has' : 'have'} it set up; the rest set it up at their next sign-in.`
                    : 'Admins set it up at their next sign-in.'}
                </div>
              </div>
              <Tooltip title="Always on: an admin account can change everything">
                <Switch checked disabled aria-label="Require MFA for admins" />
              </Tooltip>
            </div>
            <div className="flex items-start justify-between gap-6 p-4">
              <div>
                <div className="font-medium text-gray-900 dark:text-gray-100">
                  Require MFA for everyone
                </div>
                <div className="mt-1 text-gray-500 dark:text-gray-300">
                  Users without it set it up at their next sign-in
                  {users && withoutMFA > 0
                    ? ` (${withoutMFA} active user${withoutMFA === 1 ? " doesn't" : "s don't"} have it yet).`
                    : '.'}{' '}
                  Changes reach every replica within 15 seconds.
                </div>
              </div>
              <Switch
                aria-label="Require MFA for everyone"
                checked={!!settings.isMfaRequiredForEveryone}
                loading={saving}
                onChange={setEveryone}
              />
            </div>
          </div>
        )}
      </section>

      <SSOSettingsComponent />
    </div>
  );
};
