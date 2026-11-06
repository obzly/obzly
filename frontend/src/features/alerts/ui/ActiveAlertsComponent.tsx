import { LoadingOutlined, ReloadOutlined } from '@ant-design/icons';
import { App, Button, Spin, Table, Tag } from 'antd';
import dayjs from 'dayjs';
import { useEffect, useState } from 'react';

import { type AlertView, alertingApi } from '../../../entity/alerting';
import { useVisibleInterval } from '../../../shared/lib';
import { LabelTags } from './labels';
import { errorMessage } from './lib';

const STATE_COLORS: Record<AlertView['state'], string> = {
  firing: 'red',
  pending: 'orange',
  resolved: 'green',
};

export const ActiveAlertsComponent = ({ projectId }: { projectId: string }) => {
  const { message } = App.useApp();
  const [alerts, setAlerts] = useState<AlertView[]>([]);
  const [isLoading, setIsLoading] = useState(true);

  const load = async () => {
    setIsLoading(true);
    try {
      setAlerts(await alertingApi.listAlerts(projectId));
    } catch (error: unknown) {
      message.error(errorMessage(error, 'Failed to load alerts'));
    } finally {
      setIsLoading(false);
    }
  };

  useEffect(() => {
    load();
  }, [projectId]);
  useVisibleInterval(load, 30_000);

  if (isLoading && alerts.length === 0) {
    return <Spin indicator={<LoadingOutlined spin />} />;
  }

  return (
    <div>
      <div className="mb-3 flex justify-end">
        <Button icon={<ReloadOutlined />} onClick={load} loading={isLoading}>
          Refresh
        </Button>
      </div>
      <Table
        rowKey={(alert) => alert.ruleId + alert.fingerprint}
        dataSource={alerts}
        pagination={false}
        locale={{ emptyText: 'Nothing pending or firing' }}
        columns={[
          {
            title: 'State',
            dataIndex: 'state',
            width: 110,
            render: (state: AlertView['state']) => <Tag color={STATE_COLORS[state]}>{state}</Tag>,
          },
          { title: 'Rule', dataIndex: 'ruleName', width: 220 },
          {
            title: 'Labels',
            dataIndex: 'labels',
            render: (labels: Record<string, string>) => <LabelTags labels={labels} />,
          },
          {
            title: 'Value',
            dataIndex: 'value',
            width: 110,
            render: (value: number) => Number(value.toPrecision(6)),
          },
          {
            title: 'Since',
            width: 160,
            render: (_, alert) => {
              const at = alert.state === 'resolved' ? alert.resolvedAt : alert.firedAt;
              return dayjs(at ?? alert.activeAt).fromNow();
            },
          },
        ]}
      />
    </div>
  );
};
