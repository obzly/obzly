import { CopyOutlined, LoadingOutlined, PlusOutlined } from '@ant-design/icons';
import { Alert, App, Button, Input, Modal, Popconfirm, Spin, Table, Typography } from 'antd';
import dayjs from 'dayjs';
import { useEffect, useState } from 'react';

import {
  type NewSCIMToken,
  type OrgDetail,
  type SCIMToken,
  organizationsApi,
} from '../../../entity/organizations';
import { errorMessage } from './lib';

interface Props {
  org: OrgDetail;
}

export const SCIMComponent = ({ org }: Props) => {
  const { message } = App.useApp();
  const [tokens, setTokens] = useState<SCIMToken[]>([]);
  const [isLoading, setIsLoading] = useState(true);
  const [name, setName] = useState('');
  const [created, setCreated] = useState<NewSCIMToken | null>(null);

  const load = async () => {
    setIsLoading(true);
    try {
      setTokens(await organizationsApi.listSCIMTokens(org.id));
    } catch (error: unknown) {
      message.error(errorMessage(error, 'Failed to load SCIM tokens'));
    } finally {
      setIsLoading(false);
    }
  };

  const create = async () => {
    try {
      const token = await organizationsApi.createSCIMToken(org.id, name.trim());
      setCreated(token);
      setName('');
      await load();
    } catch (error: unknown) {
      message.error(errorMessage(error, 'Failed to create the token'));
    }
  };

  const revoke = async (token: SCIMToken) => {
    try {
      await organizationsApi.deleteSCIMToken(org.id, token.id);
      setTokens((prev) => prev.filter((t) => t.id !== token.id));
      message.success(`${token.name} revoked`);
    } catch (error: unknown) {
      message.error(errorMessage(error, 'Failed to revoke the token'));
    }
  };

  useEffect(() => {
    load();
  }, [org.id]);

  return (
    <div>
      <div className="mb-3 text-sm text-gray-500 dark:text-gray-300">
        Provision users and groups from your identity provider (Okta, Microsoft Entra ID) with SCIM
        2.0. Users become members of {org.name} and sign in with SSO; groups become teams, whose
        roles you set here. Deprovisioning deactivates accounts SCIM created, and suspends the
        membership of any other.
      </div>
      <div className="mb-4">
        <div className="mb-1 text-sm font-medium text-gray-900 dark:text-gray-100">Base URL</div>
        <Typography.Text code copyable>
          {organizationsApi.scimBaseUrl()}
        </Typography.Text>
      </div>
      <div className="mb-4 flex gap-2">
        <Input
          placeholder="Token name (e.g. okta)"
          value={name}
          onChange={(e) => setName(e.target.value)}
          style={{ width: 260 }}
        />
        <Button icon={<PlusOutlined />} disabled={!name.trim()} onClick={create}>
          Create token
        </Button>
      </div>
      {isLoading ? (
        <Spin indicator={<LoadingOutlined spin />} />
      ) : (
        <Table
          rowKey="id"
          dataSource={tokens}
          pagination={false}
          locale={{ emptyText: 'No SCIM tokens' }}
          columns={[
            { title: 'Name', dataIndex: 'name' },
            { title: 'Token', render: (_, t) => <code>obzscim_{t.prefix}…</code> },
            {
              title: 'Created',
              render: (_, t) => dayjs(t.createdAt).format('YYYY-MM-DD HH:mm'),
            },
            {
              title: 'Last used',
              render: (_, t) => (t.lastUsedAt ? dayjs(t.lastUsedAt).fromNow() : 'never'),
            },
            {
              title: '',
              width: 90,
              render: (_, t) => (
                <Popconfirm
                  title={`Revoke ${t.name}? The IdP stops provisioning.`}
                  okText="Revoke"
                  okButtonProps={{ danger: true }}
                  onConfirm={() => revoke(t)}
                >
                  <Button size="small" danger>
                    Revoke
                  </Button>
                </Popconfirm>
              ),
            },
          ]}
        />
      )}
      {created && (
        <Modal
          open
          title={`SCIM token ${created.name}`}
          onCancel={() => setCreated(null)}
          footer={<Button onClick={() => setCreated(null)}>Done</Button>}
        >
          <Alert
            type="warning"
            showIcon
            className="mb-3"
            message="Copy the token now - it isn't shown again."
          />
          <Typography.Paragraph
            code
            copyable={{ icon: <CopyOutlined />, text: created.token }}
            className="break-all"
          >
            {created.token}
          </Typography.Paragraph>
        </Modal>
      )}
    </div>
  );
};
