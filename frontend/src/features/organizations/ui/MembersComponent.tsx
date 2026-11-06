import { LoadingOutlined } from '@ant-design/icons';
import { App, Button, Input, Popconfirm, Select, Spin, Table, Tag } from 'antd';
import { useEffect, useState } from 'react';

import {
  type OrgDetail,
  type OrgMember,
  type OrgRole,
  organizationsApi,
} from '../../../entity/organizations';
import { errorMessage } from './lib';

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

  if (isLoading) {
    return <Spin indicator={<LoadingOutlined spin />} />;
  }

  const roleOptions = ROLES.filter((r) => canMakeOwners || r.value !== 'ORG_OWNER');

  return (
    <div>
      {canManage && (
        <div className="mb-4 flex gap-2">
          <Input
            placeholder="User email"
            value={email}
            onChange={(e) => setEmail(e.target.value)}
            style={{ width: 300 }}
          />
          <Select value={role} onChange={setRole} options={roleOptions} style={{ width: 130 }} />
          <Button disabled={!email.trim()} onClick={() => save({ email: email.trim(), role })}>
            Add
          </Button>
        </div>
      )}
      <Table
        rowKey="userId"
        dataSource={members}
        pagination={false}
        columns={[
          {
            title: 'User',
            render: (_, m) => (
              <span>
                {m.name} ({m.email})
                {m.suspended && (
                  <Tag color="orange" className="ml-2">
                    suspended by SCIM
                  </Tag>
                )}
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
                ROLES.find((r) => r.value === m.role)?.label
              ),
          },
          {
            title: '',
            width: 90,
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
    </div>
  );
};
