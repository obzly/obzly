import { LoadingOutlined } from '@ant-design/icons';
import { App, Spin, Switch, Tooltip } from 'antd';
import { useEffect, useState } from 'react';

import { settingsApi } from '../../../entity/users/api/settingsApi';
import { userManagementApi } from '../../../entity/users/api/userManagementApi';
import type { UserProfile } from '../../../entity/users/model/UserProfile';
import { UserRole } from '../../../entity/users/model/UserRole';
import type { UsersSettings } from '../../../entity/users/model/UsersSettings';
import { Card, Gauge } from '../../../shared/ui/v2';
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
  const withMFA = active.length - withoutMFA;

  return (
    <div className="flex flex-col gap-4">
      <Card
        title="Multi-factor authentication"
        subtitle="TOTP authenticator apps. Sign-ins through an SSO connector set to trust its provider's MFA don't ask again."
      >
        {!settings ? (
          <Spin indicator={<LoadingOutlined spin />} />
        ) : (
          <div className="flex flex-wrap items-center gap-6">
            <div className="flex min-w-0 flex-[1_1_380px] flex-col gap-3 text-sm">
              <div className="flex items-start justify-between gap-6">
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
              <div className="flex items-start justify-between gap-6 border-t border-gray-200 pt-3 dark:border-gray-700">
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
            {/* Coverage from the same users list; unknown until it loads. */}
            <div className="flex items-center gap-3">
              <Gauge
                size={96}
                ratio={users && active.length > 0 ? withMFA / active.length : null}
                value={
                  users && active.length > 0
                    ? `${Math.round((withMFA / active.length) * 100)}%`
                    : 'n/a'
                }
                label="Active users with MFA set up"
              />
              <div className="flex flex-col text-sm">
                <span className="text-gray-900 dark:text-gray-100">
                  {users
                    ? `${withMFA} of ${active.length} active user${active.length === 1 ? '' : 's'} have MFA on`
                    : 'No data'}
                </span>
                {users && (
                  <span className="text-xs text-gray-500 dark:text-gray-400">
                    {adminsWithMFA} of {admins.length} admin{admins.length === 1 ? '' : 's'}
                  </span>
                )}
              </div>
            </div>
          </div>
        )}
      </Card>

      <SSOSettingsComponent />
    </div>
  );
};
