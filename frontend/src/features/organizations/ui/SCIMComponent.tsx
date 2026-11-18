import { CopyOutlined, LoadingOutlined, PlusOutlined, WarningOutlined } from '@ant-design/icons';
import { App, Button, Input, Modal, Popconfirm, Spin, Table, Typography } from 'antd';
import dayjs from 'dayjs';
import { useEffect, useState } from 'react';

import {
  type NewSCIMToken,
  type OrgDetail,
  type SCIMEvent,
  type SCIMToken,
  organizationsApi,
} from '../../../entity/organizations';
import { Card } from '../../../shared/ui/v2';
import { type PillTone, WARN_CLASS, errorMessage, pillClass } from './lib';

interface Props {
  org: OrgDetail;
}

// Audit messages read "SCIM (<token>): <change>"; the verb picks the pill.
const describeEvent = (message: string): { op: string; tone: PillTone; what: string } => {
  const match = /^SCIM \(([^)]*)\): (.*)$/.exec(message);
  const what = match ? match[2] : message;
  const token = match ? match[1] : '';
  const suffix = token ? ` (token ${token})` : '';
  if (/ created$/.test(what)) return { op: 'Created', tone: 'ok', what: what + suffix };
  if (/ (deactivated|suspended)$/.test(what))
    return { op: 'Suspended', tone: 'warn', what: what + suffix };
  if (/ (activated|unsuspended)$/.test(what))
    return { op: 'Restored', tone: 'ok', what: what + suffix };
  if (/ (removed from organization|deleted)$/.test(what))
    return { op: 'Removed', tone: 'bad', what: what + suffix };
  return { op: 'Updated', tone: 'info', what: what + suffix };
};

export const SCIMComponent = ({ org }: Props) => {
  const { message } = App.useApp();
  const [tokens, setTokens] = useState<SCIMToken[]>([]);
  const [isLoading, setIsLoading] = useState(true);
  const [name, setName] = useState('');
  const [created, setCreated] = useState<NewSCIMToken | null>(null);
  // null when they couldn't be loaded.
  const [events, setEvents] = useState<SCIMEvent[] | null>([]);

  const load = async () => {
    setIsLoading(true);
    organizationsApi
      .listSCIMEvents(org.id)
      .then(setEvents)
      .catch(() => setEvents(null));
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

  // Derived from the tokens: whether the IdP has called in with one yet.
  const state = isLoading
    ? null
    : tokens.some((t) => t.lastUsedAt)
      ? { text: 'In use', tone: 'ok' as const }
      : tokens.length > 0
        ? { text: 'Not used yet', tone: 'warn' as const }
        : { text: 'Not set up', tone: 'neutral' as const };

  return (
    <div>
      <Card
        title="SCIM provisioning"
        subtitle={
          <>
            Provision users and groups from your identity provider (Okta, Microsoft Entra ID) with
            SCIM 2.0. Users become members of {org.name} and sign in with SSO; groups become teams,
            whose roles you set here. Deprovisioning deactivates accounts SCIM created, and suspends
            the membership of any other.
          </>
        }
        extra={state && <span className={pillClass(state.tone)}>{state.text}</span>}
        bodyClassName="flex flex-col gap-4"
      >
        <div className="flex flex-col gap-1">
          <div className="text-sm text-gray-600 dark:text-gray-300">Base URL</div>
          <div className="w-fit max-w-full rounded-[10px] border border-gray-200 bg-gray-50 px-3 py-1.5 break-all dark:border-gray-700 dark:bg-gray-900/40">
            <Typography.Text code copyable>
              {organizationsApi.scimBaseUrl()}
            </Typography.Text>
          </div>
        </div>
        <div className="flex flex-wrap items-center gap-2">
          <Input
            placeholder="Token name (e.g. okta)"
            value={name}
            onChange={(e) => setName(e.target.value)}
            className="min-w-0 flex-[1_1_220px]"
            style={{ maxWidth: 300 }}
          />
          <Button type="primary" icon={<PlusOutlined />} disabled={!name.trim()} onClick={create}>
            Create token
          </Button>
        </div>
        {isLoading ? (
          <Spin indicator={<LoadingOutlined spin />} />
        ) : (
          <Table
            rowKey="id"
            size="small"
            dataSource={tokens}
            pagination={false}
            scroll={{ x: 560 }}
            locale={{ emptyText: 'No SCIM tokens' }}
            columns={[
              { title: 'Name', dataIndex: 'name' },
              {
                title: 'Token',
                render: (_, t) => (
                  <code className="text-xs text-gray-700 dark:text-gray-200">
                    obzscim_{t.prefix}…
                  </code>
                ),
              },
              {
                title: 'Created',
                render: (_, t) => (
                  <span className="text-gray-500 dark:text-gray-400">
                    {dayjs(t.createdAt).format('YYYY-MM-DD HH:mm')}
                  </span>
                ),
              },
              {
                title: 'Last used',
                render: (_, t) => (
                  <span className="text-gray-500 dark:text-gray-400">
                    {t.lastUsedAt ? dayjs(t.lastUsedAt).fromNow() : 'never'}
                  </span>
                ),
              },
              {
                title: '',
                width: 90,
                align: 'right',
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
        <div className="flex flex-col gap-2 border-t border-gray-200 pt-3 dark:border-gray-700">
          <strong className="text-sm font-semibold text-gray-900 dark:text-gray-100">
            Recent changes
          </strong>
          {events === null ? (
            <span className="text-sm text-gray-500 dark:text-gray-400">
              Couldn&apos;t load recent changes
            </span>
          ) : events.length === 0 ? (
            <span className="text-sm text-gray-500 dark:text-gray-400">
              No SCIM changes recorded yet
            </span>
          ) : (
            <ul className="m-0 flex list-none flex-col gap-1.5 p-0">
              {events.map((event) => {
                const { op, tone, what } = describeEvent(event.message);
                return (
                  <li
                    key={event.id}
                    className="flex flex-wrap items-center gap-x-2.5 gap-y-1 text-sm"
                  >
                    <span
                      className="w-24 shrink-0 text-gray-500 dark:text-gray-400"
                      title={dayjs(event.createdAt).format('YYYY-MM-DD HH:mm')}
                    >
                      {dayjs(event.createdAt).fromNow()}
                    </span>
                    <span className={pillClass(tone)}>{op}</span>
                    <span className="min-w-0 break-words text-gray-800 dark:text-gray-200">
                      {what}
                    </span>
                  </li>
                );
              })}
            </ul>
          )}
        </div>
      </Card>
      {created && (
        <Modal
          open
          title={`SCIM token ${created.name}`}
          onCancel={() => setCreated(null)}
          footer={<Button onClick={() => setCreated(null)}>Done</Button>}
        >
          <div role="alert" className={`${WARN_CLASS} mb-3 flex items-center gap-2`}>
            <WarningOutlined />
            Copy the token now - it isn&apos;t shown again.
          </div>
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
