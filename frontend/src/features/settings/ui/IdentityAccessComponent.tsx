import { SafetyCertificateOutlined } from '@ant-design/icons';
import { useEffect, useState } from 'react';

import { userManagementApi } from '../../../entity/users/api/userManagementApi';
import type { UserProfile } from '../../../entity/users/model/UserProfile';
import { UserRole } from '../../../entity/users/model/UserRole';
import { Card, Gauge } from '../../../shared/ui/v2';
import { SSOSettingsComponent } from './SSOSettingsComponent';

// Only feeds counts here; the Users page paginates properly.
const USERS_LIMIT = 1000;

export const IdentityAccessComponent = () => {
  const [users, setUsers] = useState<UserProfile[]>();

  useEffect(() => {
    userManagementApi
      .getUsers({ limit: USERS_LIMIT })
      .then((response) => setUsers(response.users))
      .catch(() => setUsers([]));
  }, []);

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
        <div className="flex flex-wrap items-center gap-6">
          <div className="flex min-w-0 flex-[1_1_380px] flex-col gap-3 text-sm">
            <div className="flex items-start gap-3">
              <SafetyCertificateOutlined className="mt-0.5 text-lg text-emerald-600 dark:text-emerald-400" />
              <div>
                <div className="font-medium text-gray-900 dark:text-gray-100">
                  MFA is required for everyone
                </div>
                <div className="mt-1 text-gray-500 dark:text-gray-300">
                  Every account, whatever its role, sets MFA up at its next sign-in and can&apos;t
                  turn it off
                  {users && withoutMFA > 0
                    ? ` (${withoutMFA} active user${withoutMFA === 1 ? " doesn't" : "s don't"} have it yet).`
                    : '.'}{' '}
                  If someone loses their device, an admin can reset it on the Users page.
                </div>
              </div>
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
      </Card>

      <SSOSettingsComponent />
    </div>
  );
};
