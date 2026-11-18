import { LoadingOutlined, SettingOutlined } from '@ant-design/icons';
import { App, Button, Spin, Table, Tooltip } from 'antd';
import { useEffect, useState } from 'react';

import { type ChannelKind, type NotificationChannel, alertingApi } from '../../../entity/alerting';
import { Card } from '../../../shared/ui/v2';
import { KindIcon } from './KindIcon';
import { Pill } from './bits';
import { errorMessage } from './lib';

interface Props {
  projectId: string;
  canEdit: boolean;
  onManageChannels?: () => void;
}

/** The channels this project's rules can use; they are managed in Obzly settings. */
export const ChannelsComponent = ({ projectId, canEdit, onManageChannels }: Props) => {
  const { message } = App.useApp();
  const [channels, setChannels] = useState<NotificationChannel[]>([]);
  const [isLoading, setIsLoading] = useState(true);
  const [testing, setTesting] = useState<string | null>(null);

  const load = async () => {
    setIsLoading(true);
    try {
      setChannels(await alertingApi.listChannels(projectId));
    } catch (error: unknown) {
      message.error(errorMessage(error, 'Failed to load channels'));
    } finally {
      setIsLoading(false);
    }
  };

  const test = async (channel: NotificationChannel) => {
    setTesting(channel.id);
    try {
      await alertingApi.testChannel(projectId, channel.id);
      message.success(`Test notification sent to ${channel.name}`);
    } catch (error: unknown) {
      message.error(errorMessage(error, 'Test notification failed'));
    } finally {
      setTesting(null);
    }
  };

  useEffect(() => {
    load();
  }, [projectId]);

  if (isLoading) {
    return <Spin indicator={<LoadingOutlined spin />} />;
  }

  return (
    <Card
      title="Channels"
      subtitle={
        <>
          Channels are managed by admins in Obzly settings → Notification channels and shared across
          projects. These are the ones this project&apos;s rules can use.
        </>
      }
      extra={
        onManageChannels && (
          <Button icon={<SettingOutlined />} onClick={onManageChannels}>
            Manage channels
          </Button>
        )
      }
    >
      <Table
        rowKey="id"
        dataSource={channels}
        pagination={false}
        scroll={{ x: 'max-content' }}
        locale={{ emptyText: 'No channels available to this project yet' }}
        columns={[
          {
            title: 'Name',
            dataIndex: 'name',
            render: (name: string, channel) => (
              <span className="flex flex-wrap items-center gap-2 font-medium text-gray-900 dark:text-gray-50">
                <KindIcon kind={channel.kind} />
                {name}
                {channel.templateError && (
                  <Tooltip title={channel.templateError}>
                    <Pill tone="warn" tabIndex={0}>
                      template error
                    </Pill>
                  </Tooltip>
                )}
              </span>
            ),
          },
          {
            title: 'Kind',
            dataIndex: 'kind',
            width: 120,
            render: (kind: ChannelKind) => <Pill tone="muted">{kind}</Pill>,
          },
          {
            title: 'Sends to',
            dataIndex: 'target',
            render: (target: string) => <span className="font-mono text-xs">{target}</span>,
          },
          {
            title: 'Managed by',
            dataIndex: 'source',
            width: 140,
            render: (source: string) => (
              <span className="text-gray-500 dark:text-gray-400">
                {source === 'central' ? 'Control plane' : 'This Site'}
              </span>
            ),
          },
          {
            title: '',
            width: 90,
            align: 'right',
            render: (_, channel) =>
              canEdit && (
                <Button size="small" loading={testing === channel.id} onClick={() => test(channel)}>
                  Test
                </Button>
              ),
          },
        ]}
      />
    </Card>
  );
};
