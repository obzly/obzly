import { LoadingOutlined, UserAddOutlined } from '@ant-design/icons';
import { App, Button, Drawer, Input, Popconfirm, Select, Spin, Switch, Table, Tooltip } from 'antd';
import type { ColumnsType } from 'antd/es/table';
import dayjs from 'dayjs';
import { useCallback, useEffect, useRef, useState } from 'react';

import { mfaApi } from '../../../entity/users/api/mfaApi';
import { userManagementApi } from '../../../entity/users/api/userManagementApi';
import type { ChangeUserRoleRequest } from '../../../entity/users/model/ChangeUserRoleRequest';
import type { ListUsersRequest } from '../../../entity/users/model/ListUsersRequest';
import type { UserProfile } from '../../../entity/users/model/UserProfile';
import { UserRole } from '../../../entity/users/model/UserRole';
import { getUserShortTimeFormat } from '../../../shared/time';
import { InviteUserModalComponent } from './InviteUserModalComponent';
import { SetUserPasswordModalComponent } from './SetUserPasswordModalComponent';
import { UserAuditLogsSidebarComponent } from './UserAuditLogsSidebarComponent';

interface Props {
  contentHeight: number;
  // The signed-in user, who can't delete their own account.
  currentUserId?: string;
}

const getRoleColor = (role: UserRole): string => {
  switch (role) {
    case UserRole.ADMIN:
      return '#3b82f6';
    case UserRole.MEMBER:
      return '#10b981';
    default:
      return '#6b7280';
  }
};

export function UsersComponent({ contentHeight, currentUserId }: Props) {
  const { message } = App.useApp();
  const [users, setUsers] = useState<UserProfile[]>([]);
  const [isLoading, setIsLoading] = useState(true);
  const [isLoadingMore, setIsLoadingMore] = useState(false);
  const [hasMore, setHasMore] = useState(true);
  const [total, setTotal] = useState(0);
  const [searchQuery, setSearchQuery] = useState('');
  const [inputValue, setInputValue] = useState('');

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
  }, [isLoadingMore, hasMore]);

  useEffect(() => {
    const scrollContainer = scrollContainerRef.current;
    if (scrollContainer) {
      scrollContainer.addEventListener('scroll', handleScroll);
      return () => scrollContainer.removeEventListener('scroll', handleScroll);
    }
  }, [handleScroll]);

  const loadUsers = async (isInitialLoad = false, query?: string) => {
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
        <div>
          {record.name} ({record.email})
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
          style={{
            backgroundColor: isActive ? '#059669' : undefined,
          }}
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
        <div className="flex gap-2">
          <Button type="primary" ghost size="small" onClick={() => handleRowClick(record)}>
            View audit logs
          </Button>
          <Button size="small" onClick={() => setSettingPasswordForUser(record)}>
            Set password
          </Button>
          <Button
            danger
            ghost
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
    <div className="flex grow pl-3">
      <div className="w-full">
        <div
          ref={scrollContainerRef}
          className="grow overflow-y-auto rounded bg-white p-5 shadow dark:bg-gray-800"
          style={{ height: contentHeight }}
        >
          <div className="mb-4 flex items-center justify-between">
            <h1 className="text-2xl font-bold">Obzly Users</h1>
            <div className="text-sm text-gray-500 dark:text-gray-300">
              {isLoading ? 'Loading...' : `${users.length} of ${total} users`}
            </div>
          </div>

          <div className="mb-4 flex items-center gap-3">
            <Input
              placeholder="Search by email or name..."
              allowClear
              value={inputValue}
              onChange={(e) => setInputValue(e.target.value)}
              style={{ width: 400 }}
            />
            <Button type="primary" icon={<UserAddOutlined />} onClick={() => setIsInviteOpen(true)}>
              Invite user
            </Button>
          </div>

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
                className="mb-4"
              />

              {isLoadingMore && (
                <div className="flex justify-center py-4">
                  <Spin indicator={<LoadingOutlined spin />} />
                </div>
              )}

              {!hasMore && users.length > 0 && (
                <div className="py-4 text-center text-sm text-gray-500 dark:text-gray-300">
                  All users loaded ({total} total)
                </div>
              )}
            </>
          )}
        </div>
      </div>

      <Drawer
        title={
          <div>
            <div className="text-lg font-semibold text-gray-900 dark:text-gray-100">
              User Audit Logs
            </div>
            <div className="text-sm text-gray-600 dark:text-gray-300">{selectedUser?.email}</div>
          </div>
        }
        placement="right"
        width={900}
        onClose={handleDrawerClose}
        open={isDrawerOpen}
      >
        {selectedUser && <UserAuditLogsSidebarComponent user={selectedUser} />}
      </Drawer>

      {isInviteOpen && (
        <InviteUserModalComponent
          onClose={() => setIsInviteOpen(false)}
          onInvited={() => loadUsers(true, searchQuery)}
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
    return <span className="text-sm text-gray-400">Never</span>;
  }
  const date = dayjs(value);
  return (
    <Tooltip title={date.format(getUserShortTimeFormat().format)}>
      <span className="text-sm text-gray-600 dark:text-gray-300">{date.fromNow()}</span>
    </Tooltip>
  );
}
