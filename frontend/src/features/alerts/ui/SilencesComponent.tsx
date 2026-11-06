import { DeleteOutlined, LoadingOutlined, PlusOutlined } from '@ant-design/icons';
import {
  App,
  Button,
  Checkbox,
  Input,
  Modal,
  Popconfirm,
  Select,
  Spin,
  Table,
  Tag,
  Tooltip,
} from 'antd';
import dayjs from 'dayjs';
import { useEffect, useState } from 'react';

import { type Matcher, type Silence, alertingApi } from '../../../entity/alerting';
import { errorMessage } from './lib';

interface Props {
  projectId: string;
  canEdit: boolean;
}

/** A maintenance silence matches many monitors: say how many, not every id. */
const matchesText = (silence: Silence): string =>
  silence.matchers
    .map((m) =>
      silence.managedBy && m.name === 'check_id'
        ? `${m.value.split('|').length} Synthetics monitor${m.value.includes('|') ? 's' : ''}`
        : `${m.name}${m.regex ? '=~' : '='}"${m.value}"`,
    )
    .join(', ');

const DURATIONS = [
  { value: 1, label: '1 hour' },
  { value: 4, label: '4 hours' },
  { value: 24, label: '1 day' },
  { value: 168, label: '1 week' },
];

export const SilencesComponent = ({ projectId, canEdit }: Props) => {
  const { message } = App.useApp();
  const [silences, setSilences] = useState<Silence[]>([]);
  const [isLoading, setIsLoading] = useState(true);
  const [matchers, setMatchers] = useState<Matcher[] | null>(null);
  const [hours, setHours] = useState(4);
  const [comment, setComment] = useState('');
  const [isSaving, setIsSaving] = useState(false);

  const load = async () => {
    setIsLoading(true);
    try {
      setSilences(await alertingApi.listSilences(projectId));
    } catch (error: unknown) {
      message.error(errorMessage(error, 'Failed to load silences'));
    } finally {
      setIsLoading(false);
    }
  };

  const save = async () => {
    if (!matchers) return;
    setIsSaving(true);
    try {
      const silence = await alertingApi.createSilence(projectId, {
        matchers: matchers.filter((m) => m.name.trim()),
        endsAt: dayjs().add(hours, 'hour').toISOString(),
        comment,
      });
      setSilences((prev) => [...prev, silence]);
      setMatchers(null);
      setComment('');
      message.success('Silence created');
    } catch (error: unknown) {
      message.error(errorMessage(error, 'Failed to create silence'));
    } finally {
      setIsSaving(false);
    }
  };

  const remove = async (silence: Silence) => {
    try {
      await alertingApi.deleteSilence(projectId, silence.id);
      setSilences((prev) => prev.filter((s) => s.id !== silence.id));
      message.success('Silence removed');
    } catch (error: unknown) {
      message.error(errorMessage(error, 'Failed to remove silence'));
    }
  };

  useEffect(() => {
    load();
  }, [projectId]);

  if (isLoading) {
    return <Spin indicator={<LoadingOutlined spin />} />;
  }

  const setMatcher = (index: number, changes: Partial<Matcher>) =>
    setMatchers((prev) => prev && prev.map((m, i) => (i === index ? { ...m, ...changes } : m)));

  return (
    <div>
      {canEdit && (
        <div className="mb-3 flex justify-end">
          <Button
            type="primary"
            icon={<PlusOutlined />}
            onClick={() => setMatchers([{ name: 'alertname', value: '' }])}
            className="border-emerald-600 bg-emerald-600 hover:border-emerald-700 hover:bg-emerald-700"
          >
            New silence
          </Button>
        </div>
      )}
      <Table
        rowKey="id"
        dataSource={silences}
        pagination={false}
        locale={{ emptyText: 'No silences' }}
        columns={[
          {
            title: 'Matches',
            render: (_, silence) => (
              <span className="inline-flex flex-wrap items-center gap-2">
                {silence.managedBy && <Tag color="default">Synthetics maintenance</Tag>}
                {matchesText(silence)}
              </span>
            ),
          },
          {
            title: 'From',
            width: 160,
            render: (_, silence) =>
              dayjs(silence.startsAt).isAfter(dayjs())
                ? dayjs(silence.startsAt).format('MMM D, HH:mm')
                : 'Now',
          },
          {
            title: 'Until',
            width: 180,
            render: (_, silence) => dayjs(silence.endsAt).format('MMM D, HH:mm'),
          },
          { title: 'Comment', dataIndex: 'comment' },
          {
            title: '',
            width: 60,
            render: (_, silence) =>
              canEdit &&
              (silence.managedBy ? (
                <Tooltip title="Kept by a Synthetics maintenance window: change the window on the control plane">
                  <Button
                    size="small"
                    icon={<DeleteOutlined />}
                    disabled
                    aria-label="Managed by a maintenance window"
                  />
                </Tooltip>
              ) : (
                <Popconfirm title="End this silence now?" onConfirm={() => remove(silence)}>
                  <Button size="small" icon={<DeleteOutlined />} aria-label="End this silence" />
                </Popconfirm>
              )),
          },
        ]}
      />
      {matchers && (
        <Modal
          open
          title="New silence"
          onCancel={() => setMatchers(null)}
          onOk={save}
          okText="Silence"
          confirmLoading={isSaving}
          okButtonProps={{ disabled: !matchers.some((m) => m.name.trim() && m.value) }}
        >
          <div className="mb-2 text-xs text-gray-500 dark:text-gray-300">
            Mutes notifications of alerts whose labels match every line.
          </div>
          {matchers.map((matcher, index) => (
            <div key={index} className="mb-2 flex items-center gap-2">
              <Input
                placeholder="label"
                value={matcher.name}
                onChange={(e) => setMatcher(index, { name: e.target.value })}
                style={{ width: 150 }}
              />
              <Input
                placeholder="value"
                value={matcher.value}
                onChange={(e) => setMatcher(index, { value: e.target.value })}
              />
              <Checkbox
                checked={matcher.regex}
                onChange={(e) => setMatcher(index, { regex: e.target.checked })}
              >
                regex
              </Checkbox>
            </div>
          ))}
          <Button
            size="small"
            className="mb-3"
            onClick={() => setMatchers([...matchers, { name: '', value: '' }])}
          >
            Add matcher
          </Button>
          <div className="mb-2 flex items-center gap-2">
            <span className="text-sm">For</span>
            <Select value={hours} onChange={setHours} options={DURATIONS} style={{ width: 120 }} />
          </div>
          <Input
            placeholder="Comment (why)"
            value={comment}
            onChange={(e) => setComment(e.target.value)}
          />
        </Modal>
      )}
    </div>
  );
};
