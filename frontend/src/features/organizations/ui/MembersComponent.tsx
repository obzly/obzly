import { LoadingOutlined } from '@ant-design/icons';
import { App, Button, Input, Popconfirm, Select, Spin, Table, Tooltip } from 'antd';
import dayjs from 'dayjs';
import { useEffect, useState } from 'react';

import {
  type OrgDetail,
  type OrgMember,
  type OrgRole,
  organizationsApi,
} from '../../../entity/organizations';
import { Card } from '../../../shared/ui/v2';
import { AVATAR_CLASS, errorMessage, initialsOf, pillClass } from './lib';

interface Props {
  org: OrgDetail;
  canManage: boolean;
  canMakeOwners: boolean;
}

const ROLES: { value: OrgRole; label: string }[] = [
  { value: 'ORG_OWNER', label: 'Owner' },
  { value: 'ORG_ADMIN', label: 'Admin' },
  { value: 'ORG_MEMBER', label: 'Member' },
];

export const MembersComponent = ({ org, canManage, canMakeOwners }: Props) => {
  const { message } = App.useApp();
  const [members, setMembers] = useState<OrgMember[]>([]);
  const [isLoading, setIsLoading] = useState(true);
  const [email, setEmail] = useState('');
  const [role, setRole] = useState<OrgRole>('ORG_MEMBER');

  const load = async () => {
    setIsLoading(true);
    try {
      setMembers(await organizationsApi.listMembers(org.id));
    } catch (error: unknown) {
      message.error(errorMessage(error, 'Failed to load members'));
    } finally {
      setIsLoading(false);
    }
  };

  const save = async (member: { userId?: string; email?: string; role: OrgRole }) => {
    try {
      await organizationsApi.setMember(org.id, member);
      setEmail('');
      message.success('Member saved');
      await load();
    } catch (error: unknown) {
      message.error(errorMessage(error, 'Failed to save member'));
    }
  };

  const remove = async (member: OrgMember) => {
    try {
      await organizationsApi.removeMember(org.id, member.userId);
      setMembers((prev) => prev.filter((m) => m.userId !== member.userId));
      message.success(`${member.email} removed`);
    } catch (error: unknown) {
      message.error(errorMessage(error, 'Failed to remove member'));
    }
  };

  useEffect(() => {
    load();
  }, [org.id]);

  const roleOptions = ROLES.filter((r) => canMakeOwners || r.value !== 'ORG_OWNER');

  return (
    <Card
      title="Members"
      subtitle={
        isLoading ? undefined : `${members.length} member${members.length === 1 ? '' : 's'}`
      }
      bodyClassName="flex flex-col gap-4"
    >
      {canManage && (
        <div className="flex flex-wrap items-center gap-2">
          <Input
            placeholder="User email"
            value={email}
            onChange={(e) => setEmail(e.target.value)}
            className="min-w-0 flex-[1_1_220px]"
            style={{ maxWidth: 320 }}
          />
          <Select value={role} onChange={setRole} options={roleOptions} style={{ width: 130 }} />
          <Button
            type="primary"
            disabled={!email.trim()}
            onClick={() => save({ email: email.trim(), role })}
          >
            Add
          </Button>
        </div>
      )}
      {isLoading ? (
        <Spin indicator={<LoadingOutlined spin />} />
      ) : (
        <Table
          rowKey="userId"
          size="small"
          dataSource={members}
          pagination={false}
          scroll={{ x: 820 }}
          columns={[
            {
              title: 'User',
              render: (_, m) => (
                <span className="flex items-center gap-2.5">
                  <span className={AVATAR_CLASS} aria-hidden="true">
                    {initialsOf(m.name, m.email)}
                  </span>
                  <span className="flex min-w-0 flex-wrap items-center gap-x-1.5 gap-y-0.5">
                    <span className="text-gray-900 dark:text-gray-100">{m.name}</span>
                    <span className="text-gray-500 dark:text-gray-400">({m.email})</span>
                    {m.suspended && <span className={pillClass('warn')}>suspended by SCIM</span>}
                  </span>
                </span>
              ),
            },
            {
              title: 'Role',
              width: 170,
              render: (_, m) =>
                canManage && (canMakeOwners || m.role !== 'ORG_OWNER') ? (
                  <Select
                    size="small"
                    value={m.role}
                    onChange={(value) => save({ userId: m.userId, role: value })}
                    options={roleOptions}
                    style={{ width: 120 }}
                  />
                ) : (
                  <span className={pillClass(m.role === 'ORG_MEMBER' ? 'neutral' : 'ok')}>
                    {ROLES.find((r) => r.value === m.role)?.label}
                  </span>
                ),
            },
            {
              title: 'Teams',
              render: (_, m) =>
                m.teams === undefined ? (
                  <span className="text-gray-400 dark:text-gray-500">n/a</span>
                ) : m.teams.length === 0 ? (
                  <span className="text-gray-400 dark:text-gray-500">None</span>
                ) : (
                  <span className="flex flex-wrap gap-1">
                    {m.teams.map((team) => (
                      <span key={team} className={pillClass('neutral')}>
                        {team}
                      </span>
                    ))}
                  </span>
                ),
            },
            {
              title: 'Joined',
              width: 130,
              render: (_, m) =>
                m.createdAt ? (
                  <Tooltip title={dayjs(m.createdAt).format('YYYY-MM-DD HH:mm')}>
                    <span className="text-gray-500 dark:text-gray-400">
                      {dayjs(m.createdAt).format('MMM D, YYYY')}
                    </span>
                  </Tooltip>
                ) : (
                  <span className="text-gray-400 dark:text-gray-500">n/a</span>
                ),
            },
            {
              title: '',
              width: 90,
              align: 'right',
              render: (_, m) =>
                canManage && (canMakeOwners || m.role !== 'ORG_OWNER') ? (
                  <Popconfirm
                    title={`Remove ${m.email} from ${org.name} and its teams?`}
                    okText="Remove"
                    okButtonProps={{ danger: true }}
                    onConfirm={() => remove(m)}
                  >
                    <Button size="small" danger>
                      Remove
                    </Button>
                  </Popconfirm>
                ) : null,
            },
          ]}
        />
      )}
    </Card>
  );
};
