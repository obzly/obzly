import { LoadingOutlined } from '@ant-design/icons';
import {
  Alert,
  App,
  Button,
  Input,
  Modal,
  Popconfirm,
  Select,
  Spin,
  Table,
  Typography,
} from 'antd';
import { type JSX, useEffect, useState } from 'react';

import { getApplicationServer } from '../../../constants';
import {
  type AccessToken,
  type OAuthConnectedApp,
  accessTokenApi,
  mfaApi,
  oauthApi,
} from '../../../entity/users';
import { StringUtils } from '../../../shared/lib';

const EXPIRY_OPTIONS = [
  { value: 30, label: '30 days' },
  { value: 90, label: '90 days' },
  { value: 365, label: '1 year' },
];

const formatDate = (value?: string | null): string =>
  value ? new Date(value).toLocaleDateString() : 'Never';

const mcpUrl = (): string => `${getApplicationServer()}/mcp`;

const claudeCodeCommand = (token: string): string =>
  `claude mcp add --transport http obzly ${mcpUrl()} --header "Authorization: Bearer ${token}"`;

const jsonConfig = (token: string): string =>
  JSON.stringify(
    { mcpServers: { obzly: { url: mcpUrl(), headers: { Authorization: `Bearer ${token}` } } } },
    null,
    2,
  );

/** Personal access tokens for AI assistants (the MCP endpoint), plus setup snippets. */
export const AccessTokensComponent = (): JSX.Element => {
  const { message } = App.useApp();

  const [tokens, setTokens] = useState<AccessToken[]>([]);
  const [isLoading, setLoading] = useState(true);
  const [mfaEnabled, setMfaEnabled] = useState(false);
  const [isCreateOpen, setCreateOpen] = useState(false);
  const [name, setName] = useState('');
  const [expiresInDays, setExpiresInDays] = useState(90);
  const [mfaCode, setMfaCode] = useState('');
  const [isCreating, setCreating] = useState(false);
  const [createError, setCreateError] = useState('');
  const [createdToken, setCreatedToken] = useState<string | null>(null);
  const [apps, setApps] = useState<OAuthConnectedApp[]>([]);

  const loadTokens = async () => {
    try {
      setTokens(await accessTokenApi.list());
    } catch (e) {
      message.error(StringUtils.capitalizeFirstLetter((e as Error).message));
    } finally {
      setLoading(false);
    }
  };

  const loadApps = async () => {
    try {
      setApps(await oauthApi.listConnectedApps());
    } catch {
      setApps([]);
    }
  };

  const revokeApp = async (app: OAuthConnectedApp) => {
    try {
      await oauthApi.revokeConnectedApp(app.id);
      message.success(`Disconnected ${app.clientName}`);
      await loadApps();
    } catch (e) {
      message.error(StringUtils.capitalizeFirstLetter((e as Error).message));
    }
  };

  const openCreate = () => {
    setName('');
    setExpiresInDays(90);
    setMfaCode('');
    setCreateError('');
    setCreateOpen(true);
  };

  const create = async () => {
    if (!name.trim()) {
      setCreateError('Give the token a name, e.g. "Claude Code on my laptop"');
      return;
    }
    if (mfaEnabled && mfaCode.length !== 6) {
      setCreateError('Enter the 6-digit code from your authenticator app');
      return;
    }

    setCreating(true);
    setCreateError('');
    try {
      const response = await accessTokenApi.create(name.trim(), expiresInDays, mfaCode);
      setCreateOpen(false);
      setCreatedToken(response.token);
      await loadTokens();
    } catch (e) {
      setCreateError(StringUtils.capitalizeFirstLetter((e as Error).message));
    } finally {
      setCreating(false);
    }
  };

  const revoke = async (token: AccessToken) => {
    try {
      await accessTokenApi.revoke(token.id);
      message.success(`Revoked "${token.name}"`);
      await loadTokens();
    } catch (e) {
      message.error(StringUtils.capitalizeFirstLetter((e as Error).message));
    }
  };

  useEffect(() => {
    loadTokens();
    loadApps();
    mfaApi
      .getStatus()
      .then((status) => setMfaEnabled(status.enabled))
      .catch(() => setMfaEnabled(false));
  }, []);

  return (
    <div className="max-w-3xl">
      <div className="mb-3 text-sm text-gray-600 dark:text-gray-300">
        Let AI assistants such as Claude Code or Cursor query your logs, traces and metrics through
        MCP. A token has read-only access to everything your account can see, and works only on{' '}
        <code>{mcpUrl()}</code>.
      </div>

      <div className="mb-4 text-sm text-gray-600 dark:text-gray-300">
        <b>claude.ai and Claude Desktop:</b> add a custom connector with the URL{' '}
        <Typography.Text code copyable>
          {mcpUrl()}
        </Typography.Text>{' '}
        and sign in when asked. <b>Claude Code, Cursor and other tools:</b> create a token below.
      </div>

      {apps.length > 0 && (
        <div className="mb-5">
          <div className="mb-2 text-sm font-semibold">Connected apps</div>
          <Table<OAuthConnectedApp>
            size="small"
            rowKey="id"
            dataSource={apps}
            pagination={false}
            columns={[
              { title: 'App', dataIndex: 'clientName' },
              { title: 'Last refreshed', dataIndex: 'lastUsedAt', render: formatDate },
              { title: 'Access ends', dataIndex: 'expiresAt', render: formatDate },
              {
                title: '',
                key: 'actions',
                align: 'right',
                render: (_, app) => (
                  <Popconfirm
                    title={`Disconnect ${app.clientName}?`}
                    description="It loses access immediately."
                    okText="Disconnect"
                    okButtonProps={{ danger: true }}
                    onConfirm={() => revokeApp(app)}
                  >
                    <Button size="small" danger>
                      Disconnect
                    </Button>
                  </Popconfirm>
                ),
              },
            ]}
          />
        </div>
      )}

      <div className="mb-2 text-sm font-semibold">Access tokens</div>
      <Button type="primary" onClick={openCreate}>
        Create token
      </Button>

      <div className="mt-4">
        {isLoading ? (
          <Spin indicator={<LoadingOutlined spin />} />
        ) : (
          <Table<AccessToken>
            size="small"
            rowKey="id"
            dataSource={tokens}
            pagination={false}
            locale={{ emptyText: 'No active tokens' }}
            columns={[
              { title: 'Name', dataIndex: 'name' },
              { title: 'Token', dataIndex: 'tokenPrefix', render: (v: string) => <code>{v}</code> },
              { title: 'Last used', dataIndex: 'lastUsedAt', render: formatDate },
              { title: 'Expires', dataIndex: 'expiresAt', render: formatDate },
              {
                title: '',
                key: 'actions',
                align: 'right',
                render: (_, token) => (
                  <Popconfirm
                    title={`Revoke "${token.name}"?`}
                    description="Assistants using it stop working immediately."
                    okText="Revoke"
                    okButtonProps={{ danger: true }}
                    onConfirm={() => revoke(token)}
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
      </div>

      <Modal
        title="Create access token"
        open={isCreateOpen}
        onCancel={() => setCreateOpen(false)}
        onOk={create}
        okText="Create"
        confirmLoading={isCreating}
      >
        <div className="mb-1 text-xs font-semibold">Name</div>
        <Input
          placeholder="Claude Code on my laptop"
          value={name}
          maxLength={100}
          onChange={(e) => setName(e.target.value)}
          onPressEnter={create}
        />

        <div className="mt-3 mb-1 text-xs font-semibold">Expires after</div>
        <Select
          className="w-full"
          value={expiresInDays}
          options={EXPIRY_OPTIONS}
          onChange={setExpiresInDays}
        />

        {mfaEnabled && (
          <>
            <div className="mt-3 mb-1 text-xs font-semibold">Authenticator code</div>
            <Input
              placeholder="000000"
              inputMode="numeric"
              maxLength={6}
              value={mfaCode}
              onChange={(e) => setMfaCode(e.target.value.replace(/\D/g, '').slice(0, 6))}
              onPressEnter={create}
            />
          </>
        )}

        {createError && <Alert className="mt-3" type="error" message={createError} showIcon />}
      </Modal>

      <Modal
        title="Token created"
        open={createdToken !== null}
        onCancel={() => setCreatedToken(null)}
        footer={
          <Button type="primary" onClick={() => setCreatedToken(null)}>
            Done
          </Button>
        }
        width={680}
      >
        {createdToken && (
          <>
            <Alert
              type="warning"
              showIcon
              message="Copy it now - it won't be shown again."
              className="mb-3"
            />
            <Typography.Paragraph copyable={{ text: createdToken }}>
              <code className="break-all">{createdToken}</code>
            </Typography.Paragraph>

            <div className="mt-4 mb-1 text-xs font-semibold">Claude Code</div>
            <Typography.Paragraph copyable={{ text: claudeCodeCommand(createdToken) }}>
              <pre className="rounded bg-gray-50 p-2 text-xs break-all whitespace-pre-wrap dark:bg-gray-900">
                {claudeCodeCommand(createdToken)}
              </pre>
            </Typography.Paragraph>

            <div className="mb-1 text-xs font-semibold">Cursor and other clients (mcp.json)</div>
            <Typography.Paragraph copyable={{ text: jsonConfig(createdToken) }}>
              <pre className="rounded bg-gray-50 p-2 text-xs dark:bg-gray-900">
                {jsonConfig(createdToken)}
              </pre>
            </Typography.Paragraph>
          </>
        )}
      </Modal>
    </div>
  );
};
