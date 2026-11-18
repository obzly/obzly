import { CloseOutlined, DeleteOutlined, LoadingOutlined, PlusOutlined } from '@ant-design/icons';
import {
  App,
  Button,
  Checkbox,
  Input,
  Modal,
  Popconfirm,
  Segmented,
  Spin,
  Table,
  Tooltip,
} from 'antd';
import dayjs from 'dayjs';
import { useEffect, useState } from 'react';

import { type Matcher, type Silence, alertingApi } from '../../../entity/alerting';
import { Card } from '../../../shared/ui/v2';
import { Field, Pill } from './bits';
import { errorMessage } from './lib';

interface Props {
  projectId: string;
  canEdit: boolean;
  /** Opens the new-silence form with these (from a firing alert's Silence button). */
  initialMatchers?: Matcher[] | null;
  /** After a silence is created or removed (the page header counts them). */
  onChanged?: () => void;
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

export const SilencesComponent = ({ projectId, canEdit, initialMatchers, onChanged }: Props) => {
  const { message } = App.useApp();
  const [silences, setSilences] = useState<Silence[]>([]);
  const [isLoading, setIsLoading] = useState(true);
  const [matchers, setMatchers] = useState<Matcher[] | null>(
    canEdit && initialMatchers?.length ? initialMatchers : null,
  );
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
      onChanged?.();
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
      onChanged?.();
    } catch (error: unknown) {
      message.error(errorMessage(error, 'Failed to remove silence'));
    }
  };

  useEffect(() => {
    load();
  }, [projectId]);

  const setMatcher = (index: number, changes: Partial<Matcher>) =>
    setMatchers((prev) => prev && prev.map((m, i) => (i === index ? { ...m, ...changes } : m)));

  return (
    <>
      {isLoading ? (
        <Spin indicator={<LoadingOutlined spin />} />
      ) : (
        <Card
          title="Silences"
          subtitle="Mute notifications for alerts whose labels match; alerts still show under Active"
          extra={
            canEdit && (
              <Button
                type="primary"
                icon={<PlusOutlined />}
                onClick={() => setMatchers([{ name: 'alertname', value: '' }])}
                className="border-accent-600 bg-accent-600 hover:border-accent-700 hover:bg-accent-700"
              >
                New silence
              </Button>
            )
          }
        >
          <Table
            rowKey="id"
            dataSource={silences}
            pagination={false}
            scroll={{ x: 'max-content' }}
            locale={{ emptyText: 'No silences' }}
            columns={[
              {
                title: 'Matches',
                render: (_, silence) => (
                  <span className="inline-flex flex-wrap items-center gap-2">
                    {silence.managedBy && <Pill tone="ok">Synthetics maintenance</Pill>}
                    <span className="font-mono text-xs">{matchesText(silence)}</span>
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
              {
                title: 'Comment',
                dataIndex: 'comment',
                render: (value: string, silence) => (
                  <span className="flex flex-col gap-0.5">
                    <span className="text-gray-500 dark:text-gray-400">{value}</span>
                    {silence.mcpClient && (
                      <span className="text-xs text-gray-400 dark:text-gray-500">
                        Created via MCP by {silence.mcpClient}
                      </span>
                    )}
                  </span>
                ),
              },
              {
                title: '',
                width: 60,
                align: 'right',
                render: (_, silence) =>
                  canEdit &&
                  (silence.managedBy ? (
                    <Tooltip title="Kept by a Synthetics maintenance window: change the window on the control plane">
                      <span className="inline-flex">
                        <Button
                          size="small"
                          icon={<DeleteOutlined />}
                          disabled
                          aria-label="Managed by a maintenance window"
                        />
                      </span>
                    </Tooltip>
                  ) : (
                    <Popconfirm title="End this silence now?" onConfirm={() => remove(silence)}>
                      <Button
                        size="small"
                        icon={<DeleteOutlined />}
                        aria-label="End this silence"
                      />
                    </Popconfirm>
                  )),
              },
            ]}
          />
        </Card>
      )}
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
          <div className="flex flex-col gap-3">
            <div className="text-[13px] text-gray-500 dark:text-gray-400">
              Mutes notifications of alerts whose labels match every line.
            </div>
            <div className="flex flex-col gap-2">
              {matchers.map((matcher, index) => (
                <div
                  key={index}
                  className="grid grid-cols-[minmax(0,1fr)_minmax(0,1fr)_auto_auto] items-center gap-2"
                >
                  <Input
                    className="font-mono"
                    placeholder="label"
                    aria-label="label"
                    value={matcher.name}
                    onChange={(e) => setMatcher(index, { name: e.target.value })}
                  />
                  <Input
                    className="font-mono"
                    placeholder="value"
                    aria-label="value"
                    value={matcher.value}
                    onChange={(e) => setMatcher(index, { value: e.target.value })}
                  />
                  <Checkbox
                    checked={matcher.regex}
                    onChange={(e) => setMatcher(index, { regex: e.target.checked })}
                  >
                    regex
                  </Checkbox>
                  <Button
                    type="text"
                    size="small"
                    icon={<CloseOutlined />}
                    aria-label="Remove matcher"
                    disabled={matchers.length === 1}
                    onClick={() => setMatchers(matchers.filter((_, i) => i !== index))}
                  />
                </div>
              ))}
            </div>
            <Button
              size="small"
              icon={<PlusOutlined />}
              className="self-start"
              onClick={() => setMatchers([...matchers, { name: '', value: '' }])}
            >
              Add matcher
            </Button>
            <Field label="For">
              <Segmented
                className="self-start"
                value={hours}
                onChange={(value) => setHours(value)}
                options={DURATIONS}
              />
            </Field>
            <Field label="Comment">
              <Input
                placeholder="Comment (why)"
                value={comment}
                onChange={(e) => setComment(e.target.value)}
              />
            </Field>
          </div>
        </Modal>
      )}
    </>
  );
};
