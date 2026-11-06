import { LoadingOutlined } from '@ant-design/icons';
import { App, Button, Spin, Table, Tag } from 'antd';
import { useEffect, useState } from 'react';

import { type ChannelKind, type NotificationChannel, alertingApi } from '../../../entity/alerting';
import { errorMessage } from './lib';

interface Props {
  projectId: string;
  canEdit: boolean;
}

/** The channels this project's rules can use; they are managed in Obzly settings. */
export const ChannelsComponent = ({ projectId, canEdit }: Props) => {
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
    <div>
      <div className="mb-3 text-sm text-gray-500 dark:text-gray-300">
        Channels are managed by admins in Obzly settings → Notification channels and shared across
        projects. These are the ones this project&apos;s rules can use.
      </div>
      <Table
        rowKey="id"
        dataSource={channels}
        pagination={false}
        locale={{ emptyText: 'No channels available to this project yet' }}
        columns={[
          { title: 'Name', dataIndex: 'name' },
          {
            title: 'Kind',
            dataIndex: 'kind',
            width: 120,
            render: (kind: ChannelKind) => <Tag>{kind}</Tag>,
          },
          { title: 'Sends to', dataIndex: 'target' },
          {
            title: 'Managed by',
            dataIndex: 'source',
            width: 140,
            render: (source: string) => (source === 'central' ? 'Control plane' : 'This Site'),
          },
          {
            title: '',
            width: 90,
            render: (_, channel) =>
              canEdit && (
                <Button size="small" loading={testing === channel.id} onClick={() => test(channel)}>
                  Test
                </Button>
              ),
          },
        ]}
      />
    </div>
  );
};
