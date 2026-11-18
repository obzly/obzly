import { LoadingOutlined, SearchOutlined, UserAddOutlined } from '@ant-design/icons';
import {
  App,
  Button,
  Drawer,
  Input,
  Popconfirm,
  Segmented,
  Select,
  Spin,
  Switch,
  Table,
  Tooltip,
} from 'antd';
import type { ColumnsType } from 'antd/es/table';
import dayjs from 'dayjs';
import { useCallback, useEffect, useRef, useState } from 'react';

import { mfaApi } from '../../../entity/users/api/mfaApi';
import { userManagementApi } from '../../../entity/users/api/userManagementApi';
import type { ChangeUserRoleRequest } from '../../../entity/users/model/ChangeUserRoleRequest';
import type { ListUsersRequest } from '../../../entity/users/model/ListUsersRequest';
import type { UserProfile } from '../../../entity/users/model/UserProfile';
import { UserRole } from '../../../entity/users/model/UserRole';
import type { UsersSummary } from '../../../entity/users/model/UsersSummary';
import { getUserShortTimeFormat } from '../../../shared/time';
import { Card, PageHeader } from '../../../shared/ui/v2';
import { InviteUserModalComponent } from './InviteUserModalComponent';
import { SetUserPasswordModalComponent } from './SetUserPasswordModalComponent';
import { UserAuditLogsSidebarComponent } from './UserAuditLogsSidebarComponent';
import { UserDetailDrawerComponent } from './UserDetailDrawerComponent';
import { UsersSummaryComponent } from './UsersSummaryComponent';

interface Props {
  contentHeight: number;
  // The signed-in user, who can't delete their own account.
  currentUserId?: string;
}

const getRoleColor = (role: UserRole): string => {
  switch (role) {
    case UserRole.ADMIN:
      return 'var(--v2-blue)';
    case UserRole.MEMBER:
      return 'var(--v2-accent)';
    default:
      return 'var(--v2-muted)';
  }
};

const initialsOf = (user: UserProfile): string => {
  const source = (user.name || user.email || '?').trim();
  const parts = source.split(/[\s@._-]+/).filter(Boolean);
  return ((parts[0]?.[0] ?? '?') + (parts[1]?.[0] ?? '')).toUpperCase();
};

type UserFilter = 'all' | 'admins' | 'nomfa' | 'inactive';

// Server-side filters; "Inactive" is the backend's: deactivated or invited, or
// no sign-in or request for 30 days.
const FILTERS: { value: UserFilter; label: string; hint: string }[] = [
  { value: 'all', label: 'All', hint: 'Every user' },
  { value: 'admins', label: 'Admins', hint: 'System role Admin' },
  { value: 'nomfa', label: 'No MFA', hint: 'Active accounts without an authenticator' },
  {
    value: 'inactive',
    label: 'Inactive',
    hint: 'Deactivated or not signed up, or no sign-in for 30 days',
  },
];

const filterParams = (filter: UserFilter): Partial<ListUsersRequest> => {
  switch (filter) {
    case 'admins':
      return { role: UserRole.ADMIN };
    case 'nomfa':
      return { mfa: 'off', status: 'ACTIVE' };
    case 'inactive':
      return { activity: 'inactive' };
    default:
      return {};
  }
};

// Clicks on a row's own controls don't open the details panel, nor do clicks
// in their popups (portals: outside the row's DOM, but React bubbles them).
const isRowControl = (row: Element, target: EventTarget | null) =>
  !(target instanceof Element) ||
  !row.contains(target) ||
  !!target.closest('button, a, input, .ant-select, .ant-switch');

const PILL = 'inline-flex items-center gap-1 rounded-full px-2 py-0.5 text-xs font-medium';
const PILL_NEUTRAL = `${PILL} bg-gray-100 text-gray-600 dark:bg-gray-700 dark:text-gray-300`;

export function UsersComponent({ contentHeight, currentUserId }: Props) {
  const { message } = App.useApp();
  const [users, setUsers] = useState<UserProfile[]>([]);
  const [isLoading, setIsLoading] = useState(true);
  const [isLoadingMore, setIsLoadingMore] = useState(false);
  const [hasMore, setHasMore] = useState(true);
  const [total, setTotal] = useState(0);
  const [searchQuery, setSearchQuery] = useState('');
  const [inputValue, setInputValue] = useState('');
  const [filter, setFilter] = useState<UserFilter>('all');
  // undefined while loading, null if it failed.
  const [summary, setSummary] = useState<UsersSummary | null | undefined>(undefined);
  const [detailUser, setDetailUser] = useState<UserProfile | null>(null);

  const pageSize = 20;

  const [processingUsers, setProcessingUsers] = useState<Set<string>>(new Set());
  const [changingRoleUsers, setChangingRoleUsers] = useState<Set<string>>(new Set());
  const [resettingMfaUsers, setResettingMfaUsers] = useState<Set<string>>(new Set());
  const [deletingUsers, setDeletingUsers] = useState<Set<string>>(new Set());

  const [selectedUser, setSelectedUser] = useState<UserProfile | null>(null);
  const [isDrawerOpen, setIsDrawerOpen] = useState(false);
  const [settingPasswordForUser, setSettingPasswordForUser] = useState<UserProfile | null>(null);
  const [isInviteOpen, setIsInviteOpen] = useState(false);

  const scrollContainerRef = useRef<HTMLDivElement>(null);
  const loadingRef = useRef(false);

  useEffect(() => {
    loadUsers(true);
    loadSummary();
  }, []);

  useEffect(() => {
    const timer = setTimeout(() => {
      if (inputValue !== searchQuery) {
        setSearchQuery(inputValue);
        setHasMore(true);
        loadUsers(true, inputValue);
      }
    }, 500);

    return () => clearTimeout(timer);
  }, [inputValue]);

  const handleScroll = useCallback(() => {
    if (!scrollContainerRef.current || isLoadingMore || !hasMore || loadingRef.current) return;

    const { scrollTop, scrollHeight, clientHeight } = scrollContainerRef.current;
    const threshold = 100;

    if (scrollHeight - scrollTop - clientHeight < threshold) {
      loadUsers(false);
    }
  }, [isLoadingMore, hasMore, users.length, searchQuery, filter]);

  useEffect(() => {
    const scrollContainer = scrollContainerRef.current;
    if (scrollContainer) {
      scrollContainer.addEventListener('scroll', handleScroll);
      return () => scrollContainer.removeEventListener('scroll', handleScroll);
    }
  }, [handleScroll]);

  const loadSummary = () => {
    userManagementApi
      .getUsersSummary()
      .then(setSummary)
      .catch(() => setSummary(null));
  };

  const changeFilter = (next: UserFilter) => {
    setFilter(next);
    setHasMore(true);
    loadUsers(true, searchQuery, next);
  };

  const loadUsers = async (isInitialLoad = false, query?: string, filterOverride?: UserFilter) => {
    if (!isInitialLoad && loadingRef.current) {
      return;
    }

    loadingRef.current = true;

    if (isInitialLoad) {
      setIsLoading(true);
      setUsers([]);
    } else {
      setIsLoadingMore(true);
    }

    try {
      const offset = isInitialLoad ? 0 : users.length;
      const currentQuery = query !== undefined ? query : searchQuery;
      const request: ListUsersRequest = {
        limit: pageSize,
        offset: offset,
        query: currentQuery || undefined,
        ...filterParams(filterOverride ?? filter),
      };

      const response = await userManagementApi.getUsers(request);

      if (isInitialLoad) {
        setUsers(response.users);
      } else {
        setUsers((prev) => {
          const existingIds = new Set(prev.map((user) => user.id));
          const newUsers = response.users.filter((user) => !existingIds.has(user.id));
          return [...prev, ...newUsers];
        });
      }

      setTotal(response.total);
      setHasMore(response.users.length === pageSize);
    } catch (error: unknown) {
      const errorMessage = error instanceof Error ? error.message : 'Failed to load users';
      message.error(errorMessage);
    } finally {
      loadingRef.current = false;
      setIsLoading(false);
      setIsLoadingMore(false);
    }
  };

  const handleActivationToggle = async (userId: string, isActive: boolean) => {
    setUsers((prev) =>
      prev.map((user) => (user.id === userId ? { ...user, isActive: !isActive } : user)),
    );

    setProcessingUsers((prev) => new Set(prev).add(userId));

    try {
      if (isActive) {
        await userManagementApi.deactivateUser(userId);
        message.success('User deactivated successfully');
      } else {
        await userManagementApi.activateUser(userId);
        message.success('User activated successfully');
      }
      loadSummary();
    } catch (error: unknown) {
      const errorMessage = error instanceof Error ? error.message : 'Operation failed';
      message.error(errorMessage);

      setUsers((prev) =>
        prev.map((user) => (user.id === userId ? { ...user, isActive: isActive } : user)),
      );
    } finally {
      setProcessingUsers((prev) => {
        const newSet = new Set(prev);
        newSet.delete(userId);
        return newSet;
      });
    }
  };

  const handleRoleChange = async (userId: string, newRole: UserRole) => {
    const currentUser = users.find((user) => user.id === userId);
    const originalRole = currentUser?.role;

    setUsers((prev) =>
      prev.map((user) => (user.id === userId ? { ...user, role: newRole } : user)),
    );

    setChangingRoleUsers((prev) => new Set(prev).add(userId));

    try {
      const request: ChangeUserRoleRequest = { role: newRole };
      await userManagementApi.changeUserRole(userId, request);
      message.success('User role changed successfully');
      loadSummary();
    } catch (error: unknown) {
      const errorMessage = error instanceof Error ? error.message : 'Failed to change user role';
      message.error(errorMessage);

      if (originalRole) {
        setUsers((prev) =>
          prev.map((user) => (user.id === userId ? { ...user, role: originalRole } : user)),
        );
      }
    } finally {
      setChangingRoleUsers((prev) => {
        const newSet = new Set(prev);
        newSet.delete(userId);
        return newSet;
      });
    }
  };

  const handleResetMfa = async (userId: string) => {
    setResettingMfaUsers((prev) => new Set(prev).add(userId));

    try {
      await mfaApi.resetUserMFA(userId);
      message.success('MFA reset successfully - the user will need to set it up again');
      loadSummary();
    } catch (error: unknown) {
      const errorMessage = error instanceof Error ? error.message : 'Failed to reset MFA';
      message.error(errorMessage);
    } finally {
      setResettingMfaUsers((prev) => {
        const newSet = new Set(prev);
        newSet.delete(userId);
        return newSet;
      });
    }
  };

  const handleDeleteUser = async (userId: string) => {
    setDeletingUsers((prev) => new Set(prev).add(userId));

    try {
      await userManagementApi.deleteUser(userId);
      setUsers((prev) => prev.filter((user) => user.id !== userId));
      setTotal((prev) => Math.max(prev - 1, 0));
      message.success('User deleted');
      loadSummary();
    } catch (error: unknown) {
      const errorMessage = error instanceof Error ? error.message : 'Failed to delete user';
      message.error(errorMessage);
    } finally {
      setDeletingUsers((prev) => {
        const newSet = new Set(prev);
        newSet.delete(userId);
        return newSet;
      });
    }
  };

  const handlePasswordSet = (userId: string) => {
    // Setting a password also activates an INVITED account server-side.
    setUsers((prev) =>
      prev.map((user) => (user.id === userId ? { ...user, isActive: true } : user)),
    );
  };

  const handleRowClick = (user: UserProfile) => {
    setSelectedUser(user);
    setIsDrawerOpen(true);
  };

  const handleDrawerClose = () => {
    setIsDrawerOpen(false);
    setSelectedUser(null);
  };

  const columns: ColumnsType<UserProfile> = [
    {
      title: 'User',
      key: 'user',
      width: 350,
      render: (_, record: UserProfile) => (
        <div className="flex items-center gap-3">
          <span className="flex h-8 w-8 shrink-0 items-center justify-center rounded-full bg-[var(--v2-ok-soft)] text-xs font-semibold text-[var(--v2-accent)]">
            {initialsOf(record)}
          </span>
          <div className="min-w-0">
            <button
              type="button"
              onClick={() => setDetailUser(record)}
              aria-label={`Details for ${record.email}`}
              className="cursor-pointer border-0 bg-transparent p-0 text-left text-gray-900 hover:underline dark:text-gray-50"
            >
              {record.name}{' '}
              <span className="text-gray-500 dark:text-gray-400">({record.email})</span>
            </button>
            <div className="mt-0.5 flex flex-wrap items-center gap-2 text-xs text-gray-500 dark:text-gray-400">
              {record.mfaEnabled !== undefined && (
                <span className="inline-flex items-center gap-1.5">
                  <span
                    className="inline-block h-2 w-2 rounded-full"
                    style={{
                      background: record.mfaEnabled ? 'var(--v2-accent)' : 'var(--v2-orange)',
                    }}
                  />
                  MFA {record.mfaEnabled ? 'on' : 'off'}
                </span>
              )}
              {record.id === currentUserId && <span className={PILL_NEUTRAL}>you</span>}
            </div>
          </div>
        </div>
      ),
    },
    {
      title: 'System role',
      dataIndex: 'role',
      key: 'role',
      width: 200,
      render: (role: UserRole, record: UserProfile) => (
        <Select
          value={role}
          onChange={(value) => handleRoleChange(record.id, value)}
          loading={changingRoleUsers.has(record.id)}
          disabled={changingRoleUsers.has(record.id)}
          size="small"
          className="w-24"
          style={{
            color: getRoleColor(role),
          }}
          options={[
            {
              label: <span style={{ color: getRoleColor(UserRole.ADMIN) }}>Admin</span>,
              value: UserRole.ADMIN,
            },
            {
              label: <span style={{ color: getRoleColor(UserRole.MEMBER) }}>Member</span>,
              value: UserRole.MEMBER,
            },
          ]}
        />
      ),
    },
    {
      title: 'Is active?',
      dataIndex: 'isActive',
      key: 'isActive',
      width: 200,
      render: (isActive: boolean, record: UserProfile) => (
        <Switch
          checked={isActive}
          onChange={() => handleActivationToggle(record.id, isActive)}
          loading={processingUsers.has(record.id)}
          disabled={processingUsers.has(record.id)}
          size="small"
        />
      ),
    },
    {
      title: 'Last login',
      dataIndex: 'lastLoginAt',
      key: 'lastLoginAt',
      width: 160,
      sorter: (a, b) => compareOptionalTimes(a.lastLoginAt, b.lastLoginAt),
      render: (lastLoginAt?: string | null) => <RelativeTimeCell value={lastLoginAt} />,
    },
    {
      title: 'Last active',
      dataIndex: 'lastActiveAt',
      key: 'lastActiveAt',
      width: 160,
      sorter: (a, b) => compareOptionalTimes(a.lastActiveAt, b.lastActiveAt),
      render: (lastActiveAt?: string | null) => <RelativeTimeCell value={lastActiveAt} />,
    },
    {
      title: 'Created',
      dataIndex: 'createdAt',
      key: 'createdAt',
      width: 300,
      render: (createdAt: string) => {
        const date = dayjs(createdAt);
        const timeFormat = getUserShortTimeFormat();
        return (
          <div className="text-sm text-gray-600 dark:text-gray-300">
            <div>{date.format(timeFormat.format)}</div>
            <div className="text-xs text-gray-400 dark:text-gray-400">{date.fromNow()}</div>
          </div>
        );
      },
    },
    {
      title: '',
      key: 'empty',
      render: (_, record: UserProfile) => (
        <div className="flex flex-wrap justify-end gap-2">
          <Button size="small" onClick={() => handleRowClick(record)}>
            View audit logs
          </Button>
          <Button size="small" onClick={() => setSettingPasswordForUser(record)}>
            Set password
          </Button>
          <Button
            size="small"
            loading={resettingMfaUsers.has(record.id)}
            disabled={resettingMfaUsers.has(record.id)}
            onClick={() => handleResetMfa(record.id)}
          >
            Reset MFA
          </Button>
          {record.id !== currentUserId && (
            <Popconfirm
              title={`Delete ${record.email}?`}
              description="This permanently removes the account and its project memberships."
              okText="Delete"
              okButtonProps={{ danger: true }}
              onConfirm={() => handleDeleteUser(record.id)}
            >
              <Button
                danger
                size="small"
                loading={deletingUsers.has(record.id)}
                disabled={deletingUsers.has(record.id)}
              >
                Delete
              </Button>
            </Popconfirm>
          )}
        </div>
      ),
    },
  ];

  return (
    <div className="flex grow">
      <div className="w-full">
        <div
          ref={scrollContainerRef}
          className="grow overflow-y-auto rounded-2xl border border-gray-200 bg-white p-5 dark:border-gray-700 dark:bg-gray-800"
          style={{ height: contentHeight }}
        >
          <div className="flex flex-col gap-5">
            <PageHeader
              title="Obzly Users"
              subtitle={isLoading ? 'Loading...' : `${users.length} of ${total} users`}
              actions={
                <>
                  <Input
                    placeholder="Search by email or name..."
                    allowClear
                    prefix={<SearchOutlined className="text-gray-400" />}
                    value={inputValue}
                    onChange={(e) => setInputValue(e.target.value)}
                    style={{ width: 360, maxWidth: '100%' }}
                  />
                  <Button
                    type="primary"
                    icon={<UserAddOutlined />}
                    onClick={() => setIsInviteOpen(true)}
                  >
                    Invite user
                  </Button>
                </>
              }
            />

            <UsersSummaryComponent summary={summary} />

            <Card
              title="All users"
              subtitle="Hover a login time for the exact time; open a user's audit logs from their row; click a user for sign-in and access details"
              extra={
                <Segmented<UserFilter>
                  aria-label="Filter users"
                  value={filter}
                  onChange={changeFilter}
                  options={FILTERS.map((f) => ({
                    value: f.value,
                    label: <Tooltip title={f.hint}>{f.label}</Tooltip>,
                  }))}
                />
              }
            >
              {isLoading ? (
                <div className="flex h-64 items-center justify-center">
                  <Spin indicator={<LoadingOutlined spin />} size="large" />
                </div>
              ) : (
                <>
                  <Table
                    columns={columns}
                    dataSource={users}
                    pagination={false}
                    rowKey="id"
                    size="small"
                    scroll={{ x: 1100 }}
                    rowClassName="cursor-pointer"
                    onRow={(record) => ({
                      onClick: (event) => {
                        if (!isRowControl(event.currentTarget, event.target)) setDetailUser(record);
                      },
                    })}
                    locale={
                      filter === 'all' ? undefined : { emptyText: 'No users match this filter' }
                    }
                  />

                  {isLoadingMore && (
                    <div className="flex justify-center py-4">
                      <Spin indicator={<LoadingOutlined spin />} />
                    </div>
                  )}

                  {!hasMore && users.length > 0 && (
                    <div className="pt-4 text-center text-sm text-gray-500 dark:text-gray-400">
                      All users loaded ({total} total)
                    </div>
                  )}
                </>
              )}
            </Card>
          </div>
        </div>
      </div>

      <Drawer
        title={
          <div className="flex items-center gap-3">
            {selectedUser && (
              <span className="flex h-8 w-8 shrink-0 items-center justify-center rounded-full bg-[var(--v2-ok-soft)] text-xs font-semibold text-[var(--v2-accent)]">
                {initialsOf(selectedUser)}
              </span>
            )}
            <div className="min-w-0">
              <div className="text-[15px] font-semibold text-gray-900 dark:text-gray-50">
                User Audit Logs
              </div>
              <div className="text-xs font-normal text-gray-500 dark:text-gray-400">
                {selectedUser?.email}
              </div>
            </div>
          </div>
        }
        placement="right"
        width={900}
        onClose={handleDrawerClose}
        open={isDrawerOpen}
      >
        {selectedUser && <UserAuditLogsSidebarComponent user={selectedUser} />}
      </Drawer>

      <UserDetailDrawerComponent
        user={detailUser}
        onClose={() => setDetailUser(null)}
        onViewAuditLogs={handleRowClick}
        onSetPassword={setSettingPasswordForUser}
        onResetMfa={(user) => handleResetMfa(user.id)}
        isResettingMfa={!!detailUser && resettingMfaUsers.has(detailUser.id)}
      />

      {isInviteOpen && (
        <InviteUserModalComponent
          onClose={() => setIsInviteOpen(false)}
          onInvited={() => {
            loadUsers(true, searchQuery);
            loadSummary();
          }}
        />
      )}

      {settingPasswordForUser && (
        <SetUserPasswordModalComponent
          user={settingPasswordForUser}
          onClose={() => setSettingPasswordForUser(null)}
          onPasswordSet={() => handlePasswordSet(settingPasswordForUser.id)}
        />
      )}
    </div>
  );
}

// "Never" sorts as the oldest.
function compareOptionalTimes(a?: string | null, b?: string | null): number {
  return (a ? dayjs(a).valueOf() : 0) - (b ? dayjs(b).valueOf() : 0);
}

function RelativeTimeCell({ value }: { value?: string | null }) {
  if (!value) {
    return <span className="text-sm text-gray-400 dark:text-gray-500">Never</span>;
  }
  const date = dayjs(value);
  return (
    <Tooltip title={date.format(getUserShortTimeFormat().format)}>
      <span className="text-sm text-gray-600 dark:text-gray-300">{date.fromNow()}</span>
    </Tooltip>
  );
}
