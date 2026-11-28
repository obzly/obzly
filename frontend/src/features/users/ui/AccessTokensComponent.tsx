import { LoadingOutlined } from '@ant-design/icons';
import {
  Alert,
  App,
  Button,
  Checkbox,
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
  MCP_WRITE_SCOPE,
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

// Write turned off on the server: tokens that hold the scope are served read-only.
const accessLabel = (writeAvailable: boolean, scopes?: string[]): string =>
  writeAvailable && scopes?.includes(MCP_WRITE_SCOPE) ? 'Read & write' : 'Read-only';

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
  const [allowWrite, setAllowWrite] = useState(false);
  const [writeAvailable, setWriteAvailable] = useState(false);
  const [isCreating, setCreating] = useState(false);
  const [createError, setCreateError] = useState('');
  const [createdToken, setCreatedToken] = useState<string | null>(null);
  const [apps, setApps] = useState<OAuthConnectedApp[]>([]);

  const loadTokens = async () => {
    try {
      const response = await accessTokenApi.list();
      setTokens(response.accessTokens);
      setWriteAvailable(response.writeAvailable);
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
    setAllowWrite(false);
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
      const response = await accessTokenApi.create(name.trim(), expiresInDays, mfaCode, allowWrite);
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
    <div className="flex max-w-4xl flex-col gap-4">
      <div className="text-sm text-gray-500 dark:text-gray-400">
        Let AI assistants such as Claude Code or Cursor query your logs, traces and metrics through
        MCP. A token has read-only access to everything your account can see, unless you also allow
        changes when you create it, and works only on <code>{mcpUrl()}</code>.
      </div>

      <div className="rounded-xl bg-gray-50 px-3.5 py-3 text-[13.5px] text-gray-600 dark:bg-gray-900/40 dark:text-gray-300">
        <b className="font-semibold text-gray-900 dark:text-gray-50">
          claude.ai and Claude Desktop:
        </b>{' '}
        add a custom connector with the URL{' '}
        <Typography.Text code copyable>
          {mcpUrl()}
        </Typography.Text>{' '}
        and sign in when asked.{' '}
        <b className="font-semibold text-gray-900 dark:text-gray-50">
          Claude Code, Cursor and other tools:
        </b>{' '}
        create a token below.
      </div>

      {apps.length > 0 && (
        <div className="flex flex-col gap-2 border-t border-gray-200 pt-3 dark:border-gray-700">
          <div className="text-sm font-semibold text-gray-900 dark:text-gray-50">
            Connected apps
          </div>
          <Table<OAuthConnectedApp>
            size="small"
            rowKey="id"
            dataSource={apps}
            pagination={false}
            columns={[
              { title: 'App', dataIndex: 'clientName' },
              {
                title: 'Access',
                dataIndex: 'scopes',
                render: (scopes?: string[]) => accessLabel(writeAvailable, scopes),
              },
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

      <div className="flex flex-col gap-2 border-t border-gray-200 pt-3 dark:border-gray-700">
        <div className="flex flex-wrap items-center justify-between gap-2">
          <div className="text-sm font-semibold text-gray-900 dark:text-gray-50">Access tokens</div>
          <Button type="primary" onClick={openCreate}>
            Create token
          </Button>
        </div>
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
              {
                title: 'Token',
                dataIndex: 'tokenPrefix',
                render: (v: string) => (
                  <code className="text-xs text-gray-600 dark:text-gray-300">{v}</code>
                ),
              },
              {
                title: 'Access',
                dataIndex: 'scopes',
                render: (scopes?: string[]) => accessLabel(writeAvailable, scopes),
              },
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
        <div className="mb-1 pt-1 text-xs font-medium text-gray-500 dark:text-gray-400">Name</div>
        <Input
          placeholder="Claude Code on my laptop"
          value={name}
          maxLength={100}
          onChange={(e) => setName(e.target.value)}
          onPressEnter={create}
        />

        <div className="mt-3 mb-1 text-xs font-medium text-gray-500 dark:text-gray-400">
          Expires after
        </div>
        <Select
          className="w-full"
          value={expiresInDays}
          options={EXPIRY_OPTIONS}
          onChange={setExpiresInDays}
        />

        {writeAvailable && (
          <div className="mt-3 rounded-lg border border-gray-200 p-3 dark:border-gray-700">
            <Checkbox checked={allowWrite} onChange={(e) => setAllowWrite(e.target.checked)}>
              <span className="text-sm text-gray-800 dark:text-gray-100">
                Also allow changes (write access)
              </span>
            </Checkbox>
            <div className="mt-1 text-xs text-gray-500 dark:text-gray-400">
              Lets the assistant create and end alert silences (up to 4 hours), create dashboards
              and add panels. It can&apos;t delete anything; each change is confirmed and recorded
              in the audit log.
              {!mfaEnabled &&
                ' Needs two-factor authentication: turn it on under Profile first, unless you signed in through a provider that enforces it.'}
            </div>
          </div>
        )}

        {mfaEnabled && (
          <>
            <div className="mt-3 mb-1 text-xs font-medium text-gray-500 dark:text-gray-400">
              Authenticator code
            </div>
            <Input
              placeholder="000000"
              inputMode="numeric"
              maxLength={6}
              value={mfaCode}
              onChange={(e) => setMfaCode(e.target.value.replace(/\D/g, '').slice(0, 6))}
              onPressEnter={create}
              className="!font-mono"
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
            <Typography.Paragraph
              copyable={{ text: createdToken }}
              className="rounded-xl border border-gray-200 bg-gray-50 p-3 dark:border-gray-700 dark:bg-gray-900"
            >
              <code className="break-all">{createdToken}</code>
            </Typography.Paragraph>

            <div className="mt-4 mb-1 text-xs font-medium text-gray-500 dark:text-gray-400">
              Claude Code
            </div>
            <Typography.Paragraph copyable={{ text: claudeCodeCommand(createdToken) }}>
              <pre className="rounded-xl border border-gray-200 bg-gray-50 p-3 text-xs break-all whitespace-pre-wrap dark:border-gray-700 dark:bg-gray-900">
                {claudeCodeCommand(createdToken)}
              </pre>
            </Typography.Paragraph>

            <div className="mb-1 text-xs font-medium text-gray-500 dark:text-gray-400">
              Cursor and other clients (mcp.json)
            </div>
            <Typography.Paragraph copyable={{ text: jsonConfig(createdToken) }}>
              <pre className="rounded-xl border border-gray-200 bg-gray-50 p-3 text-xs dark:border-gray-700 dark:bg-gray-900">
                {jsonConfig(createdToken)}
              </pre>
            </Typography.Paragraph>
          </>
        )}
      </Modal>
    </div>
  );
};
