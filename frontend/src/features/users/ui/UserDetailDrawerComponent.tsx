import { LoadingOutlined } from '@ant-design/icons';
import { Alert, Button, Drawer, Spin } from 'antd';
import dayjs from 'dayjs';
import { type JSX, type ReactNode, useEffect, useState } from 'react';

import { userManagementApi } from '../../../entity/users/api/userManagementApi';
import type {
  UserDetail,
  UserProjectRole,
  UserSignInMethod,
  UserSiteAccess,
} from '../../../entity/users/model/UserDetail';
import type { UserProfile } from '../../../entity/users/model/UserProfile';
import { getUserShortTimeFormat } from '../../../shared/time';

interface Props {
  user: UserProfile | null;
  onClose: () => void;
  onViewAuditLogs: (user: UserProfile) => void;
  onSetPassword: (user: UserProfile) => void;
  onResetMfa: (user: UserProfile) => void;
  isResettingMfa: boolean;
}

const PILL = 'inline-flex items-center gap-1 rounded-full px-2 py-0.5 text-xs font-medium';
const PILL_NEUTRAL = `${PILL} bg-gray-100 text-gray-700 dark:bg-gray-700 dark:text-gray-200`;
const PILL_WARN = `${PILL} bg-orange-500/15 text-[var(--v2-orange)]`;
const PILL_OK = `${PILL} bg-[var(--v2-ok-soft)] text-[var(--v2-accent)]`;
const SECTION_TITLE =
  'm-0 mb-2 text-xs font-medium tracking-wide text-gray-500 uppercase dark:text-gray-400';

const ROLE_LABEL: Record<string, string> = {
  ADMIN: 'Site admin',
  MEMBER: 'Member on every project',
  VIEWER: 'Viewer on every project',
  OWNER: 'Project owner',
  PROJECT_ADMIN: 'Project admin',
  PROJECT_MEMBER: 'Project member',
  PROJECT_VIEWER: 'Project viewer',
  ORG_OWNER: 'Owner',
  ORG_ADMIN: 'Admin',
  ORG_MEMBER: 'Member',
};

const roleLabel = (role: string) => ROLE_LABEL[role] ?? role;

const methodLabel = (method: UserSignInMethod) => {
  switch (method.kind) {
    case 'password':
      return 'Password';
    case 'sso':
      return `SSO · ${method.name ?? 'connector'}`;
    case 'github':
      return 'GitHub';
    case 'google':
      return 'Google';
  }
};

const exactTime = (value: string | null) =>
  value ? dayjs(value).format(getUserShortTimeFormat().format) : 'Never';

const relative = (value: string | null) => (value ? ` · ${dayjs(value).fromNow()}` : '');

const via = (item: { via: string; team?: string; org?: string }) => {
  switch (item.via) {
    case 'team':
      return `through team ${item.team}${item.org ? ` (${item.org})` : ''}`;
    case 'sso':
      return 'from an SSO group';
    case 'control-plane':
      return 'from the control plane';
    default:
      return 'given directly';
  }
};

const siteWhere = (grant: UserSiteAccess) =>
  `${grant.siteName || grant.siteId} · ${
    grant.projectId ? `project ${grant.projectId.slice(0, 8)}` : 'every project'
  }`;

const projectWhere = (role: UserProjectRole) =>
  role.projectId ? role.projectName || role.projectId.slice(0, 8) : 'Every project';

export const UserDetailDrawerComponent = ({
  user,
  onClose,
  onViewAuditLogs,
  onSetPassword,
  onResetMfa,
  isResettingMfa,
}: Props): JSX.Element => {
  const [detail, setDetail] = useState<UserDetail | null>(null);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    if (!user) return;
    let cancelled = false;
    setDetail(null);
    setError(null);
    userManagementApi
      .getUserDetail(user.id)
      .then((value) => {
        if (!cancelled) setDetail(value);
      })
      .catch((err: unknown) => {
        if (!cancelled) setError(err instanceof Error ? err.message : 'Failed to load the user');
      });
    return () => {
      cancelled = true;
    };
  }, [user?.id]);

  const row = (label: string, value: ReactNode) => (
    <>
      <dt className="text-gray-500 dark:text-gray-400">{label}</dt>
      <dd className="m-0 break-words text-gray-900 dark:text-gray-100">{value}</dd>
    </>
  );

  const accessRow = (key: string, where: string, role: string, how: string) => (
    <li key={key} className="flex flex-wrap items-center justify-between gap-2 py-1.5 text-sm">
      <span className="min-w-0">
        <span className="text-gray-900 dark:text-gray-100">{where}</span>
        <span className="ml-1.5 text-xs text-gray-500 dark:text-gray-400">{how}</span>
      </span>
      <span className={PILL_NEUTRAL}>{roleLabel(role)}</span>
    </li>
  );

  const origin = detail
    ? detail.provisionedBy
      ? `provisioned by SCIM (${detail.provisionedBy.name})`
      : detail.isRootAdmin
        ? 'root admin'
        : 'local account'
    : '';

  const noAccess = detail && detail.siteAccess.length === 0 && detail.projectRoles.length === 0;

  return (
    <Drawer
      title={
        <div className="min-w-0">
          <div className="text-[15px] font-semibold text-gray-900 dark:text-gray-50">
            {user?.name || user?.email}
          </div>
          <div className="text-xs font-normal break-all text-gray-500 dark:text-gray-400">
            {user?.email}
            {origin && ` · ${origin}`}
          </div>
        </div>
      }
      placement="right"
      width={560}
      onClose={onClose}
      open={!!user}
    >
      {error && <Alert type="error" showIcon message={error} />}
      {!error && !detail && (
        <div className="flex h-40 items-center justify-center">
          <Spin indicator={<LoadingOutlined spin />} />
        </div>
      )}
      {detail && user && (
        <div className="flex flex-col gap-6">
          <dl className="m-0 grid grid-cols-[minmax(110px,35%)_1fr] gap-x-3 gap-y-1.5 text-sm">
            {row(
              'Status',
              detail.status === 'ACTIVE' ? (
                <span className={PILL_OK}>Active</span>
              ) : detail.status === 'INVITED' ? (
                <span className={PILL_WARN}>Invited, not signed up</span>
              ) : (
                <span className={PILL_NEUTRAL}>Deactivated</span>
              ),
            )}
            {row('System role', detail.role === 'ADMIN' ? 'Admin' : 'Member')}
            {row(
              'Signs in with',
              detail.signInMethods.length > 0 ? (
                <span className="flex flex-wrap gap-1.5">
                  {detail.signInMethods.map((m) => (
                    <span key={`${m.kind}:${m.name ?? ''}`} className={PILL_NEUTRAL}>
                      {methodLabel(m)}
                    </span>
                  ))}
                </span>
              ) : (
                <span className="text-gray-500 dark:text-gray-400">No sign-in method yet</span>
              ),
            )}
            {row('Last sign-in', `${exactTime(detail.lastLoginAt)}${relative(detail.lastLoginAt)}`)}
            {row(
              'Last active',
              `${exactTime(detail.lastActiveAt)}${relative(detail.lastActiveAt)}`,
            )}
            {row(
              'MFA',
              detail.mfaEnabled ? (
                <span className={PILL_OK}>On</span>
              ) : (
                <span className={detail.mfaRequired ? PILL_WARN : PILL_NEUTRAL}>
                  Off{detail.mfaRequired ? ' · required at next sign-in' : ''}
                </span>
              ),
            )}
            {row('Created', exactTime(detail.createdAt))}
          </dl>

          <section>
            <h3 className={SECTION_TITLE}>Organizations</h3>
            {detail.organizations.length === 0 ? (
              <p className="m-0 text-sm text-gray-500 dark:text-gray-400">In no organization.</p>
            ) : (
              <ul className="m-0 list-none divide-y divide-gray-200 p-0 dark:divide-gray-700">
                {detail.organizations.map((org) => (
                  <li
                    key={org.orgId}
                    className="flex flex-wrap items-center justify-between gap-2 py-1.5 text-sm"
                  >
                    <span className="min-w-0">
                      <span className="text-gray-900 dark:text-gray-100">{org.name}</span>
                      <span className="ml-1.5 text-xs text-gray-500 dark:text-gray-400">
                        {org.teams.length > 0 ? `teams: ${org.teams.join(', ')}` : 'no teams'}
                      </span>
                    </span>
                    <span className="flex gap-1.5">
                      {org.suspended && <span className={PILL_WARN}>suspended by SCIM</span>}
                      <span className={PILL_NEUTRAL}>{roleLabel(org.role)}</span>
                    </span>
                  </li>
                ))}
              </ul>
            )}
          </section>

          <section>
            <h3 className={SECTION_TITLE}>Access</h3>
            {noAccess && (
              <p className="m-0 text-sm text-gray-500 dark:text-gray-400">
                No Site or project access.
              </p>
            )}
            {(detail.siteAccess.length > 0 || detail.projectRoles.length > 0) && (
              <ul className="m-0 list-none divide-y divide-gray-200 p-0 dark:divide-gray-700">
                {detail.siteAccess.map((grant, index) =>
                  accessRow(`site:${index}`, siteWhere(grant), grant.role, via(grant)),
                )}
                {detail.projectRoles.map((role, index) =>
                  accessRow(`project:${index}`, projectWhere(role), role.role, via(role)),
                )}
              </ul>
            )}
            {detail.projectRolesOnSites && (
              <p className="m-0 mt-2 text-xs text-gray-500 dark:text-gray-400">
                These are the Site grants this control plane gives. Project roles given on a Site
                itself, and project names, are in that Site&apos;s project settings.
              </p>
            )}
          </section>

          <div className="flex flex-wrap gap-2 border-t border-gray-200 pt-4 dark:border-gray-700">
            <Button size="small" onClick={() => onViewAuditLogs(user)}>
              View audit logs
            </Button>
            <Button size="small" onClick={() => onSetPassword(user)}>
              Set password
            </Button>
            <Button
              size="small"
              loading={isResettingMfa}
              disabled={isResettingMfa}
              onClick={() => onResetMfa(user)}
            >
              Reset MFA
            </Button>
          </div>
          <p className="m-0 -mt-4 text-xs text-gray-500 dark:text-gray-400">
            The same actions are on each row of the table.
          </p>
        </div>
      )}
    </Drawer>
  );
};
