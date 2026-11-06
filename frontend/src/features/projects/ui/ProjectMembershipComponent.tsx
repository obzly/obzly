import {
  CopyOutlined,
  DeleteOutlined,
  LoadingOutlined,
  PlusOutlined,
  SwapOutlined,
  UserAddOutlined,
  UserOutlined,
} from '@ant-design/icons';
import {
  App,
  AutoComplete,
  Button,
  Input,
  Modal,
  Popconfirm,
  Select,
  Spin,
  Table,
  Tag,
  Tooltip,
} from 'antd';
import type { ColumnsType } from 'antd/es/table';
import dayjs from 'dayjs';
import { useEffect, useState } from 'react';

import type {
  AddMemberRequest,
  AddMemberResponse,
  ChangeMemberRoleRequest,
  GetMembersResponse,
  ProjectMemberResponse,
  ProjectResponse,
  TransferOwnershipRequest,
} from '../../../entity/projects';
import { projectMembershipApi } from '../../../entity/projects';
import { AddMemberStatusEnum } from '../../../entity/projects/model/AddMemberStatus';
import type { UserProfile } from '../../../entity/users';
import { userManagementApi } from '../../../entity/users/api/userManagementApi';
import { ProjectRole } from '../../../entity/users/model/ProjectRole';
import { UserRole } from '../../../entity/users/model/UserRole';
import { StringUtils, copyToClipboard } from '../../../shared/lib';
import { getUserShortTimeFormat } from '../../../shared/time';

interface Props {
  contentHeight: number;
  projectResponse: ProjectResponse;
  user: UserProfile;
}

export function ProjectMembershipComponent({ contentHeight, projectResponse, user }: Props) {
  const { message } = App.useApp();

  const [members, setMembers] = useState<ProjectMemberResponse[]>([]);
  const [isLoadingMembers, setIsLoadingMembers] = useState(true);

  const [isAddMemberModalOpen, setIsAddMemberModalOpen] = useState(false);
  const [addMemberForm, setAddMemberForm] = useState({ email: '', role: ProjectRole.MEMBER });
  const [isAddingMember, setIsAddingMember] = useState(false);
  const [addMemberEmailError, setAddMemberEmailError] = useState(false);

  const [isInviteDialogOpen, setIsInviteDialogOpen] = useState(false);
  const [invitedEmail, setInvitedEmail] = useState('');
  const [invitedToken, setInvitedToken] = useState('');

  const [changingRoleFor, setChangingRoleFor] = useState<string | null>(null);
  const [isChangingRole, setIsChangingRole] = useState(false);

  const [isTransferOwnershipModalOpen, setIsTransferOwnershipModalOpen] = useState(false);
  const [transferForm, setTransferForm] = useState({ selectedMemberId: '' });
  const [isTransferringOwnership, setIsTransferringOwnership] = useState(false);
  const [transferMemberError, setTransferMemberError] = useState(false);

  const [removingMembers, setRemovingMembers] = useState<Set<string>>(new Set());

  const [userSearchResults, setUserSearchResults] = useState<UserProfile[]>([]);
  const [isSearchingUsers, setIsSearchingUsers] = useState(false);
  const [searchInputValue, setSearchInputValue] = useState('');

  const canManageMembers =
    user.role === UserRole.ADMIN ||
    projectResponse.userRole === ProjectRole.OWNER ||
    projectResponse.userRole === ProjectRole.ADMIN;

  const canTransferOwnership =
    user.role === UserRole.ADMIN || projectResponse.userRole === ProjectRole.OWNER;

  const eligibleMembers = members.filter((member) => {
    if (member.role === ProjectRole.OWNER) return false;

    if (member.userId === user.id || member.email === user.email) {
      return user.role === UserRole.ADMIN && projectResponse.userRole !== ProjectRole.OWNER;
    }

    return true;
  });

  const loadMembers = async () => {
    setIsLoadingMembers(true);
    try {
      const response: GetMembersResponse = await projectMembershipApi.getMembers(
        projectResponse.id,
      );
      setMembers(response.members);
    } catch (error: unknown) {
      const errorMessage =
        error instanceof Error
          ? StringUtils.capitalizeFirstLetter(error.message)
          : 'Failed to load members';
      message.error(errorMessage);
    } finally {
      setIsLoadingMembers(false);
    }
  };

  const searchUsers = async (query: string) => {
    if (user.role !== UserRole.ADMIN) return;

    setIsSearchingUsers(true);
    try {
      const response = await userManagementApi.getUsers({
        limit: 10,
        query: query || undefined,
      });
      const activeUsers = response.users.filter((u) => u.isActive);
      setUserSearchResults(activeUsers);
    } catch (error: unknown) {
      const errorMessage =
        error instanceof Error
          ? StringUtils.capitalizeFirstLetter(error.message)
          : 'Failed to search users';
      message.error(errorMessage);
      setUserSearchResults([]);
    } finally {
      setIsSearchingUsers(false);
    }
  };

  useEffect(() => {
    if (user.role !== UserRole.ADMIN || !isAddMemberModalOpen) return;

    const timer = setTimeout(() => {
      searchUsers(searchInputValue);
    }, 300);

    return () => clearTimeout(timer);
  }, [searchInputValue, isAddMemberModalOpen]);

  const buildInviteLink = (email: string, token: string): string =>
    `${window.location.origin}/?invite=${encodeURIComponent(token)}&email=${encodeURIComponent(email)}`;

  const handleCopyInviteLink = async (email: string, token: string) => {
    if (await copyToClipboard(buildInviteLink(email, token))) {
      message.success('Invite link copied to clipboard');
    } else {
      message.error('Failed to copy invite link');
    }
  };

  const handleAddMember = async () => {
    if (!addMemberForm.email.trim()) {
      setAddMemberEmailError(true);
      message.error('Email is required');
      return;
    }
    setAddMemberEmailError(false);
    setIsAddingMember(true);

    try {
      const request: AddMemberRequest = {
        email: addMemberForm.email.trim(),
        role: addMemberForm.role,
      };
      const response: AddMemberResponse = await projectMembershipApi.addMember(
        projectResponse.id,
        request,
      );

      const emailToRemember = request.email;
      setAddMemberForm({ email: '', role: ProjectRole.MEMBER });
      setIsAddMemberModalOpen(false);

      if (response.status === AddMemberStatusEnum.ADDED) {
        message.success('Member added successfully');
        loadMembers();
      } else if (response.status === AddMemberStatusEnum.INVITED) {
        setInvitedEmail(emailToRemember);
        setInvitedToken(response.inviteToken ?? '');
        setIsInviteDialogOpen(true);
        loadMembers();
      }
    } catch (error: unknown) {
      const errorMessage =
        error instanceof Error
          ? StringUtils.capitalizeFirstLetter(error.message)
          : 'Failed to add member';
      message.error(errorMessage);
    } finally {
      setIsAddingMember(false);
    }
  };

  const handleChangeRole = async (userId: string, newRole: ProjectRole) => {
    setChangingRoleFor(userId);
    setIsChangingRole(true);

    try {
      const request: ChangeMemberRoleRequest = { role: newRole };
      await projectMembershipApi.changeMemberRole(projectResponse.id, userId, request);

      setMembers((prev) =>
        prev.map((member) => (member.userId === userId ? { ...member, role: newRole } : member)),
      );

      message.success('Member role updated successfully');
    } catch (error: unknown) {
      const errorMessage =
        error instanceof Error
          ? StringUtils.capitalizeFirstLetter(error.message)
          : 'Failed to change member role';
      message.error(errorMessage);
    } finally {
      setChangingRoleFor(null);
      setIsChangingRole(false);
    }
  };

  const handleRemoveMember = async (userId: string, memberEmail: string) => {
    setRemovingMembers((prev) => new Set(prev).add(userId));

    try {
      await projectMembershipApi.removeMember(projectResponse.id, userId);
      setMembers((prev) => prev.filter((member) => member.userId !== userId));
      message.success(`Member "${memberEmail}" removed successfully`);
    } catch (error: unknown) {
      const errorMessage =
        error instanceof Error
          ? StringUtils.capitalizeFirstLetter(error.message)
          : 'Failed to remove member';
      message.error(errorMessage);
    } finally {
      setRemovingMembers((prev) => {
        const newSet = new Set(prev);
        newSet.delete(userId);
        return newSet;
      });
    }
  };

  const handleTransferOwnership = async () => {
    if (!transferForm.selectedMemberId) {
      setTransferMemberError(true);
      message.error('Please select a member to transfer ownership to');
      return;
    }

    const selectedMember = members.find(
      (member) => member.userId === transferForm.selectedMemberId,
    );
    if (!selectedMember) {
      message.error('Selected member not found');
      return;
    }

    setTransferMemberError(false);
    setIsTransferringOwnership(true);

    try {
      const request: TransferOwnershipRequest = {
        newOwnerEmail: selectedMember.email,
      };
      await projectMembershipApi.transferOwnership(projectResponse.id, request);

      setTransferForm({ selectedMemberId: '' });
      setIsTransferOwnershipModalOpen(false);
      message.success('Ownership transferred successfully');
      loadMembers();
    } catch (error: unknown) {
      const errorMessage =
        error instanceof Error
          ? StringUtils.capitalizeFirstLetter(error.message)
          : 'Failed to transfer ownership';
      message.error(errorMessage);
    } finally {
      setIsTransferringOwnership(false);
    }
  };

  const getRoleColor = (role: ProjectRole): string => {
    switch (role) {
      case ProjectRole.OWNER:
        return 'red';
      case ProjectRole.ADMIN:
        return 'orange';
      case ProjectRole.MEMBER:
        return 'blue';
      case ProjectRole.VIEWER:
        return 'default';
      default:
        return 'default';
    }
  };

  const getRoleDisplayText = (role: ProjectRole): string => {
    switch (role) {
      case ProjectRole.OWNER:
        return 'Owner';
      case ProjectRole.ADMIN:
        return 'Admin';
      case ProjectRole.MEMBER:
        return 'Member';
      case ProjectRole.VIEWER:
        return 'Viewer';
      default:
        return role;
    }
  };

  useEffect(() => {
    loadMembers();
  }, [projectResponse.id]);

  const columns: ColumnsType<ProjectMemberResponse> = [
    {
      title: 'Member',
      key: 'member',
      width: 300,
      render: (_, record: ProjectMemberResponse) => (
        <div className="flex items-center">
          <UserOutlined className="mr-2 text-gray-400 dark:text-gray-400" />
          <div>
            <div className="font-medium">{record.name}</div>
            <div className="text-xs text-gray-500 dark:text-gray-300">{record.email}</div>
          </div>
        </div>
      ),
    },
    {
      title: 'Role',
      dataIndex: 'role',
      key: 'role',
      width: 150,
      render: (role: ProjectRole, record: ProjectMemberResponse) => {
        const isCurrentUser = record.userId === user.id || record.email === user.email;

        if (canManageMembers && role !== ProjectRole.OWNER && !isCurrentUser) {
          return (
            <Select
              value={role}
              onChange={(newRole) => handleChangeRole(record.userId, newRole)}
              loading={changingRoleFor === record.userId && isChangingRole}
              disabled={changingRoleFor === record.userId && isChangingRole}
              size="small"
              style={{ width: 100 }}
              options={[
                { label: 'Admin', value: ProjectRole.ADMIN },
                { label: 'Member', value: ProjectRole.MEMBER },
                { label: 'Viewer', value: ProjectRole.VIEWER },
              ]}
            />
          );
        }
        return <Tag color={getRoleColor(role)}>{getRoleDisplayText(role)}</Tag>;
      },
    },
    {
      title: 'Joined',
      dataIndex: 'createdAt',
      key: 'createdAt',
      width: 200,
      render: (createdAt: Date) => {
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
      title: 'Actions',
      key: 'actions',
      width: 120,
      render: (_, record: ProjectMemberResponse) => {
        const isCurrentUser = record.userId === user.id || record.email === user.email;

        if (!canManageMembers || record.role === ProjectRole.OWNER || isCurrentUser) return null;

        return (
          <div className="flex items-center space-x-2">
            <Tooltip title="Remove member">
              <Popconfirm
                title="Remove member"
                description={`Are you sure you want to remove "${record.email}" from this project?`}
                onConfirm={() => handleRemoveMember(record.userId, record.email)}
                okText="Remove"
                cancelText="Cancel"
                okButtonProps={{ danger: true }}
              >
                <Button
                  type="text"
                  size="small"
                  icon={<DeleteOutlined />}
                  danger
                  loading={removingMembers.has(record.userId)}
                  disabled={removingMembers.has(record.userId)}
                />
              </Popconfirm>
            </Tooltip>
          </div>
        );
      },
    },
  ];

  return (
    <div className="flex grow pl-3">
      <div className="w-full">
        <div
          className="grow overflow-y-auto rounded bg-white p-5 shadow dark:bg-gray-800"
          style={{ height: contentHeight }}
        >
          <div className="max-w-[850px]">
            <div className="mb-6 flex items-center justify-between">
              <h1 className="text-2xl font-bold">Project members</h1>
              <div className="flex space-x-2">
                {canTransferOwnership && (
                  <Button
                    icon={<SwapOutlined />}
                    onClick={() => setIsTransferOwnershipModalOpen(true)}
                    disabled={isLoadingMembers || eligibleMembers.length === 0}
                  >
                    Transfer ownership
                  </Button>
                )}
                {canManageMembers && (
                  <Button
                    type="primary"
                    icon={<PlusOutlined />}
                    onClick={() => setIsAddMemberModalOpen(true)}
                    disabled={isLoadingMembers}
                    className="border-emerald-600 bg-emerald-600 hover:border-emerald-700 hover:bg-emerald-700"
                  >
                    Add member
                  </Button>
                )}
              </div>
            </div>

            {isLoadingMembers ? (
              <div className="flex h-64 items-center justify-center">
                <Spin indicator={<LoadingOutlined spin />} size="large" />
              </div>
            ) : (
              <div>
                <div className="mb-4 text-sm text-gray-500 dark:text-gray-300">
                  {members.length === 0
                    ? 'No members found'
                    : `${members.length} member${members.length !== 1 ? 's' : ''}`}
                </div>

                <Table
                  columns={columns}
                  dataSource={members}
                  pagination={false}
                  rowKey="id"
                  size="small"
                  locale={{
                    emptyText: (
                      <div className="py-8 text-center text-gray-500 dark:text-gray-300">
                        <div className="mb-2">No members found</div>
                        {canManageMembers && (
                          <div className="text-sm">Click &quot;Add member&quot; to get started</div>
                        )}
                      </div>
                    ),
                  }}
                />
              </div>
            )}

            {!canManageMembers && (
              <div className="mt-6 rounded-md bg-yellow-50 p-3">
                <div className="text-sm text-yellow-800">
                  You don&apos;t have permission to manage project members. Only project owners,
                  project admins and system administrators can add, edit or remove members.
                </div>
              </div>
            )}

            <Modal
              title="Add member"
              open={isAddMemberModalOpen}
              onOk={handleAddMember}
              onCancel={() => {
                setIsAddMemberModalOpen(false);
                setAddMemberForm({ email: '', role: ProjectRole.MEMBER });
                setAddMemberEmailError(false);
                setSearchInputValue('');
                setUserSearchResults([]);
              }}
              confirmLoading={isAddingMember}
              okText="Add member"
              cancelText="Cancel"
              okButtonProps={{
                className:
                  'border-emerald-600 bg-emerald-600 hover:border-emerald-700 hover:bg-emerald-700',
              }}
            >
              <div className="py-4">
                <div className="mb-4">
                  <div className="mb-2 font-medium text-gray-900 dark:text-gray-100">
                    Email address
                  </div>
                  {user.role === UserRole.ADMIN ? (
                    <AutoComplete
                      value={addMemberForm.email}
                      onChange={(value) => {
                        setAddMemberEmailError(false);
                        setAddMemberForm({
                          ...addMemberForm,
                          email: value.toLowerCase().trim(),
                        });
                        setSearchInputValue(value);
                      }}
                      onSelect={(value) => {
                        setAddMemberForm({
                          ...addMemberForm,
                          email: value.toLowerCase().trim(),
                        });
                      }}
                      onFocus={() => {
                        searchUsers('');
                      }}
                      placeholder="Enter email address"
                      status={addMemberEmailError ? 'error' : undefined}
                      options={userSearchResults.map((user) => ({
                        value: user.email,
                        label: `${user.name} (${user.email})`,
                      }))}
                      notFoundContent={
                        isSearchingUsers ? (
                          <div className="flex justify-center py-2">
                            <Spin indicator={<LoadingOutlined spin />} size="small" />
                          </div>
                        ) : null
                      }
                      style={{ width: '100%' }}
                    />
                  ) : (
                    <Input
                      value={addMemberForm.email}
                      onChange={(e) => {
                        setAddMemberEmailError(false);
                        setAddMemberForm({
                          ...addMemberForm,
                          email: e.target.value.toLowerCase().trim(),
                        });
                      }}
                      placeholder="Enter email address"
                      status={addMemberEmailError ? 'error' : undefined}
                    />
                  )}
                  <div className="mt-1 text-xs text-gray-500 dark:text-gray-300">
                    If the user exists, they will be added directly. Otherwise, an invitation will
                    be sent.
                  </div>
                </div>

                <div className="mb-4">
                  <div className="mb-2 font-medium text-gray-900 dark:text-gray-100">Role</div>
                  <Select
                    value={addMemberForm.role}
                    onChange={(role) => setAddMemberForm({ ...addMemberForm, role })}
                    style={{ width: '100%' }}
                    options={[
                      { label: 'Member', value: ProjectRole.MEMBER },
                      { label: 'Admin', value: ProjectRole.ADMIN },
                      { label: 'Viewer', value: ProjectRole.VIEWER },
                    ]}
                  />
                </div>
              </div>
            </Modal>

            <Modal
              title="User invited"
              open={isInviteDialogOpen}
              onOk={() => setIsInviteDialogOpen(false)}
              onCancel={() => setIsInviteDialogOpen(false)}
              okText="OK"
              cancelButtonProps={{ style: { display: 'none' } }}
              okButtonProps={{
                className:
                  'border-emerald-600 bg-emerald-600 hover:border-emerald-700 hover:bg-emerald-700',
              }}
            >
              <div className="py-4">
                <div className="flex items-start">
                  <UserAddOutlined className="mt-1 mr-3 text-2xl text-emerald-600" />
                  <div className="min-w-0 flex-1">
                    <div className="font-medium text-gray-900 dark:text-gray-100">
                      {invitedEmail} was invited
                    </div>
                    <div className="mt-1 text-sm text-gray-600 dark:text-gray-300">
                      There&apos;s no email system to deliver this automatically - copy the link
                      below and send it to them yourself. Once they use it to sign up, they&apos;ll
                      automatically become a member of the project.
                    </div>
                    {invitedToken && (
                      <div className="mt-3 flex items-center gap-2">
                        <Input readOnly value={buildInviteLink(invitedEmail, invitedToken)} />
                        <Button
                          icon={<CopyOutlined />}
                          onClick={() => handleCopyInviteLink(invitedEmail, invitedToken)}
                        />
                      </div>
                    )}
                  </div>
                </div>
              </div>
            </Modal>

            <Modal
              title="Transfer project ownership"
              open={isTransferOwnershipModalOpen}
              onOk={handleTransferOwnership}
              onCancel={() => {
                setIsTransferOwnershipModalOpen(false);
                setTransferForm({ selectedMemberId: '' });
                setTransferMemberError(false);
              }}
              confirmLoading={isTransferringOwnership}
              okText="Transfer ownership"
              cancelText="Cancel"
              okButtonProps={{
                danger: true,
                disabled: eligibleMembers.length === 0,
              }}
            >
              <div className="py-4">
                <div className="mb-4 rounded-md bg-yellow-50 p-3">
                  <div className="text-sm text-yellow-800">
                    <strong>Warning:</strong> This action cannot be undone. You will lose ownership
                    of this project and the new owner will have full control.
                  </div>
                </div>

                {eligibleMembers.length === 0 ? (
                  <div className="rounded-md bg-gray-50 p-4 text-center dark:bg-gray-900">
                    <div className="text-sm text-gray-600 dark:text-gray-300">
                      No members available to transfer ownership to. You need to have at least one
                      other member in the project to transfer ownership.
                    </div>
                  </div>
                ) : (
                  <div className="mb-4">
                    <div className="mb-2 font-medium text-gray-900 dark:text-gray-100">
                      Select new owner
                    </div>
                    <Select
                      value={transferForm.selectedMemberId || undefined}
                      onChange={(memberId) => {
                        setTransferMemberError(false);
                        setTransferForm({ selectedMemberId: memberId });
                      }}
                      placeholder="Select a member to transfer ownership to"
                      style={{ width: '100%' }}
                      status={transferMemberError ? 'error' : undefined}
                      options={eligibleMembers.map((member) => ({
                        label: (
                          <div className="flex items-center">
                            <UserOutlined className="mr-2 text-gray-400 dark:text-gray-400" />
                            <div>
                              <div className="font-medium">{member.name}</div>
                              <div className="text-xs text-gray-500 dark:text-gray-300">
                                {member.email}
                              </div>
                            </div>
                          </div>
                        ),
                        value: member.userId,
                      }))}
                    />
                    <div className="mt-1 text-xs text-gray-500 dark:text-gray-300">
                      The selected member will become the project owner
                    </div>
                  </div>
                )}
              </div>
            </Modal>
          </div>
        </div>
      </div>
    </div>
  );
}
