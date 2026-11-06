import {
  CaretRightOutlined,
  DeleteOutlined,
  EditOutlined,
  PlusOutlined,
  StopOutlined,
} from '@ant-design/icons';
import { App, Button, Popconfirm, Table } from 'antd';
import type { ColumnsType } from 'antd/es/table';
import { useEffect, useMemo, useState } from 'react';

import {
  type CheckView,
  type MaintenanceOccurrence,
  type MaintenanceState,
  type MaintenanceWindow,
  maintenanceApi,
} from '../../../entity/synthetics';
import { inFolder } from '../lib/folders';
import {
  MAINTENANCE_HATCH,
  calendarDays,
  coversText,
  dayStart,
  scheduleText,
} from '../lib/maintenance';
import { useNow } from '../lib/useNow';
import { MaintenanceEditor } from './MaintenanceEditor';
import { IconAction, Panel } from './parts';

interface Props {
  windows: MaintenanceWindow[];
  occurrences: MaintenanceOccurrence[];
  checks: CheckView[];
  canManage: boolean;
  onChanged: () => Promise<void> | void;
  /** A window to open in the editor (from a monitor's banner). */
  editId?: string;
  onEditOpened?: () => void;
}

const CALENDAR_DAYS = 14;

const STATE_ORDER: Record<MaintenanceState, number> = {
  active: 0,
  upcoming: 1,
  paused: 2,
  past: 3,
};

const STATE_STYLE: Record<MaintenanceState, { label: string; className: string }> = {
  active: { label: 'Active', className: 'bg-slate-600 text-white' },
  upcoming: {
    label: 'Upcoming',
    className: 'bg-blue-100 text-blue-900 dark:bg-blue-900/50 dark:text-blue-100',
  },
  paused: {
    label: 'Paused',
    className: 'bg-amber-100 text-amber-900 dark:bg-amber-900/40 dark:text-amber-100',
  },
  past: {
    label: 'Past',
    className: 'bg-gray-100 text-gray-700 dark:bg-gray-700 dark:text-gray-200',
  },
};

const dayLabel = new Intl.DateTimeFormat(undefined, { weekday: 'short', day: 'numeric' });
const clock = new Intl.DateTimeFormat(undefined, { hour: '2-digit', minute: '2-digit' });
const dateTime = new Intl.DateTimeFormat(undefined, {
  weekday: 'short',
  day: 'numeric',
  month: 'short',
  hour: '2-digit',
  minute: '2-digit',
});
const dateOnly = new Intl.DateTimeFormat(undefined, { day: 'numeric', month: 'short' });

/** "in 18 min", "in 3 h", "in 6 days". */
const fromNow = (at: number, now: number): string => {
  const minutes = Math.max(1, Math.round((at - now) / 60000));
  if (minutes < 60) return `in ${minutes} min`;
  if (minutes < 48 * 60) return `in ${Math.round(minutes / 60)} h`;
  return `in ${Math.round(minutes / 1440)} days`;
};

const nextText = (window: MaintenanceWindow, now: number): string => {
  if (window.state === 'paused') return 'Paused';
  if (window.current) {
    const end = Date.parse(window.current.end);
    return `Ends ${clock.format(end)} (${fromNow(end, now)})`;
  }
  if (window.next) {
    const start = Date.parse(window.next.start);
    return `Starts ${dateTime.format(start)} (${fromNow(start, now)})`;
  }
  return window.lastEnded ? `Ended ${dateOnly.format(Date.parse(window.lastEnded))}` : 'Ended';
};

export const MaintenanceComponent = ({
  windows,
  occurrences,
  checks,
  canManage,
  onChanged,
  editId,
  onEditOpened,
}: Props) => {
  const { message } = App.useApp();
  const [editing, setEditing] = useState<MaintenanceWindow | 'new'>();
  const [busy, setBusy] = useState<string>();

  useEffect(() => {
    if (!editId) return;
    const window = windows.find((item) => item.id === editId);
    if (window && canManage) setEditing(window);
    onEditOpened?.();
  }, [editId, windows, canManage, onEditOpened]);

  const names = useMemo(() => new Map(checks.map((check) => [check.id, check.name])), [checks]);
  const folderCount = (folder: string) => checks.filter((check) => inFolder(check, folder)).length;
  const now = useNow();
  const calendarFrom = dayStart(now);
  const calendar = useMemo(
    () => calendarDays(occurrences, calendarFrom, CALENDAR_DAYS),
    [occurrences, calendarFrom],
  );
  const calendarRows = windows.filter((window) => calendar.get(window.id)?.some(Boolean));
  const sorted = useMemo(
    () =>
      [...windows].sort(
        (a, b) =>
          STATE_ORDER[a.state] - STATE_ORDER[b.state] ||
          Date.parse(a.next?.start ?? a.lastEnded ?? '') -
            Date.parse(b.next?.start ?? b.lastEnded ?? '') ||
          a.name.localeCompare(b.name),
      ),
    [windows],
  );

  const act = async (key: string, action: () => Promise<unknown>, done: string) => {
    setBusy(key);
    try {
      await action();
      message.success(done);
      await onChanged();
    } catch (reason) {
      message.error((reason as Error).message);
    } finally {
      setBusy(undefined);
    }
  };

  const columns: ColumnsType<MaintenanceWindow> = [
    {
      title: 'State',
      key: 'state',
      width: 110,
      render: (_, window) => (
        <span
          className={`rounded-full px-2.5 py-0.5 text-xs font-semibold whitespace-nowrap ${STATE_STYLE[window.state].className}`}
        >
          {STATE_STYLE[window.state].label}
        </span>
      ),
    },
    {
      title: 'Window',
      key: 'window',
      render: (_, window) => (
        <div>
          <div className="font-semibold">{window.name}</div>
          {window.reason && (
            <div className="text-xs text-gray-500 dark:text-gray-400">{window.reason}</div>
          )}
        </div>
      ),
    },
    {
      title: 'Schedule',
      key: 'schedule',
      render: (_, window) => scheduleText(window.schedule),
    },
    {
      title: 'Covers',
      key: 'covers',
      render: (_, window) => coversText(window, names, folderCount),
    },
    {
      title: 'Next / current',
      key: 'next',
      render: (_, window) => nextText(window, now),
    },
  ];
  if (canManage) {
    columns.push({
      title: 'Actions',
      key: 'actions',
      width: 120,
      render: (_, window) => (
        <span className="inline-flex gap-0.5">
          <IconAction title="Edit" icon={<EditOutlined />} onClick={() => setEditing(window)} />
          {window.state === 'active' && (
            <IconAction
              title="End now"
              icon={<StopOutlined />}
              loading={busy === `end:${window.id}`}
              onClick={() =>
                act(
                  `end:${window.id}`,
                  () => maintenanceApi.end(window.id),
                  `Ended "${window.name}". Sites unmute its monitors within 5 minutes.`,
                )
              }
            />
          )}
          {window.state === 'upcoming' && (
            <IconAction
              title="Start now"
              icon={<CaretRightOutlined />}
              loading={busy === `start:${window.id}`}
              onClick={() =>
                act(
                  `start:${window.id}`,
                  () => maintenanceApi.start(window.id),
                  `Started "${window.name}". Sites mute its monitors within 5 minutes.`,
                )
              }
            />
          )}
          <Popconfirm
            title={`Delete "${window.name}"?`}
            okText="Delete"
            okButtonProps={{ danger: true }}
            onConfirm={() =>
              act(
                `delete:${window.id}`,
                () => maintenanceApi.remove(window.id),
                `Deleted "${window.name}"`,
              )
            }
          >
            <span>
              <IconAction
                title="Delete"
                icon={<DeleteOutlined />}
                danger
                loading={busy === `delete:${window.id}`}
              />
            </span>
          </Popconfirm>
        </span>
      ),
    });
  }

  return (
    <div className="flex flex-col gap-4">
      <div className="flex flex-wrap items-center gap-3">
        <span className="text-sm text-gray-600 dark:text-gray-300">
          During a window runs continue, monitors show Maintenance, uptime leaves it out and their
          alerts are muted.
        </span>
        {canManage && (
          <Button
            className="ml-auto"
            type="primary"
            icon={<PlusOutlined />}
            onClick={() => setEditing('new')}
          >
            New window
          </Button>
        )}
      </div>

      <Panel title={`Next ${CALENDAR_DAYS} days`}>
        <div className="overflow-x-auto">
          <div className="flex min-w-[760px] flex-col gap-1.5">
            <div className="grid grid-cols-[180px_repeat(14,minmax(0,1fr))] gap-0.5 text-[11px] text-gray-500 dark:text-gray-400">
              <span />
              {Array.from({ length: CALENDAR_DAYS }, (_, day) => (
                <span key={day} className="text-center">
                  {dayLabel.format(dayStart(now, day))}
                </span>
              ))}
            </div>
            {calendarRows.map((window) => (
              <div
                key={window.id}
                className="grid grid-cols-[180px_repeat(14,minmax(0,1fr))] items-center gap-0.5"
              >
                <span className="truncate text-sm text-gray-700 dark:text-gray-200">
                  {window.name}
                </span>
                {(calendar.get(window.id) ?? []).map((on, day) => (
                  <span
                    key={day}
                    role="img"
                    aria-label={`${window.name}, ${dayLabel.format(dayStart(now, day))}: ${on ? 'maintenance' : 'none'}`}
                    className="h-5 rounded-sm bg-gray-100 dark:bg-gray-700"
                    style={on ? { background: MAINTENANCE_HATCH } : undefined}
                  />
                ))}
              </div>
            ))}
            {calendarRows.length === 0 && (
              <span className="text-sm text-gray-500 dark:text-gray-400">
                No maintenance in the next {CALENDAR_DAYS} days.
              </span>
            )}
          </div>
        </div>
      </Panel>

      <Table<MaintenanceWindow>
        rowKey="id"
        size="middle"
        dataSource={sorted}
        columns={columns}
        pagination={false}
        scroll={{ x: 900 }}
        locale={{ emptyText: 'No maintenance windows yet' }}
      />

      {editing && (
        <MaintenanceEditor
          window={editing === 'new' ? undefined : editing}
          checks={checks}
          onClose={() => setEditing(undefined)}
          onSaved={async () => {
            setEditing(undefined);
            await onChanged();
          }}
        />
      )}
    </div>
  );
};
