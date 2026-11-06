import {
  App,
  Button,
  Checkbox,
  DatePicker,
  Drawer,
  Input,
  InputNumber,
  Segmented,
  Select,
  Switch,
} from 'antd';
import dayjs from 'dayjs';
import { useEffect, useMemo, useState } from 'react';

import {
  type CheckView,
  type MaintenanceRequest,
  type MaintenanceSchedule,
  type MaintenanceWindow,
  type Occurrence,
  type ScheduleKind,
  type Weekday,
  maintenanceApi,
} from '../../../entity/synthetics';
import { folderPaths, inFolder } from '../lib/folders';
import { WEEKDAYS, durationText, weekdayLabel } from '../lib/maintenance';

interface Props {
  window?: MaintenanceWindow;
  checks: CheckView[];
  onClose: () => void;
  onSaved: () => Promise<void> | void;
}

type Mode = 'once' | 'recurring';

const REPEATS: { value: ScheduleKind; label: string }[] = [
  { value: 'daily', label: 'Daily' },
  { value: 'weekly', label: 'Weekly' },
  { value: 'monthly_day', label: 'Monthly (day N)' },
  { value: 'monthly_weekday', label: 'Monthly (Nth weekday)' },
];

const WEEKS = [
  { value: 1, label: '1st' },
  { value: 2, label: '2nd' },
  { value: 3, label: '3rd' },
  { value: 4, label: '4th' },
  { value: -1, label: 'Last' },
];

const timeZones = (): string[] => {
  const supported = (Intl as { supportedValuesOf?: (key: string) => string[] }).supportedValuesOf;
  const zones = supported ? supported('timeZone') : [];
  return ['UTC', ...zones.filter((zone) => zone !== 'UTC')];
};

const browserZone = () => Intl.DateTimeFormat().resolvedOptions().timeZone || 'UTC';

const defaultSchedule = (): MaintenanceSchedule => ({
  kind: 'weekly',
  weekdays: ['tue'],
  startTime: '01:00',
  durationMinutes: 60,
  timeZone: browserZone(),
});

const scopeValue = (window?: MaintenanceWindow): string[] => [
  ...(window?.scope.folders ?? []).map((folder) => `folder:${folder}`),
  ...(window?.scope.monitorIds ?? []).map((id) => `monitor:${id}`),
];

/** "Tue 3 Nov, 01:00 to 02:00 UTC" in the schedule's zone (one-offs in the browser's). */
const occurrenceText = (occurrence: Occurrence, zone?: string): string => {
  const options: Intl.DateTimeFormatOptions = { timeZone: zone };
  const day = new Intl.DateTimeFormat(undefined, {
    ...options,
    weekday: 'short',
    day: 'numeric',
    month: 'short',
  });
  const time = new Intl.DateTimeFormat(undefined, {
    ...options,
    hour: '2-digit',
    minute: '2-digit',
  });
  const start = Date.parse(occurrence.start);
  const end = Date.parse(occurrence.end);
  const sameDay = day.format(start) === day.format(end);
  const minutes = Math.round((end - start) / 60000);
  return `${day.format(start)}, ${time.format(start)} to ${sameDay ? '' : `${day.format(end)}, `}${time.format(end)}${zone ? ` ${zone}` : ''} (${durationText(minutes)})`;
};

export const MaintenanceEditor = ({ window, checks, onClose, onSaved }: Props) => {
  const { message, modal } = App.useApp();
  const [name, setName] = useState(window?.name ?? '');
  const [reason, setReason] = useState(window?.reason ?? '');
  const [enabled, setEnabled] = useState(window?.enabled ?? true);
  const [all, setAll] = useState(window?.scope.all ?? false);
  const [scope, setScope] = useState<string[]>(scopeValue(window));
  const [mode, setMode] = useState<Mode>(window?.schedule.kind === 'once' ? 'once' : 'recurring');
  const [recurring, setRecurring] = useState<MaintenanceSchedule>(
    window && window.schedule.kind !== 'once' ? window.schedule : defaultSchedule(),
  );
  const [once, setOnce] = useState<{ start?: string; end?: string }>({
    start: window?.schedule.startsAt,
    end: window?.schedule.endsAt,
  });
  const [dirty, setDirty] = useState(false);
  const [saving, setSaving] = useState(false);
  const [preview, setPreview] = useState<{ occurrences: Occurrence[]; error?: string }>();

  const zones = useMemo(() => timeZones(), []);
  const schedule: MaintenanceSchedule = useMemo(
    () => (mode === 'once' ? { kind: 'once', startsAt: once.start, endsAt: once.end } : recurring),
    [mode, once, recurring],
  );
  const scheduleKey = JSON.stringify(schedule);

  useEffect(() => {
    if (schedule.kind === 'once' && (!schedule.startsAt || !schedule.endsAt)) {
      setPreview(undefined);
      return;
    }
    let cancelled = false;
    const timer = setTimeout(() => {
      maintenanceApi
        .preview(JSON.parse(scheduleKey) as MaintenanceSchedule)
        .then((result) => !cancelled && setPreview({ occurrences: result.occurrences }))
        .catch(
          (reason: Error) => !cancelled && setPreview({ occurrences: [], error: reason.message }),
        );
    }, 300);
    return () => {
      cancelled = true;
      clearTimeout(timer);
    };
  }, [scheduleKey]);

  const folders = useMemo(() => folderPaths(checks), [checks]);
  const covered = useMemo(
    () =>
      checks.filter(
        (check) =>
          all ||
          scope.some((value) =>
            value.startsWith('folder:')
              ? inFolder(check, value.slice('folder:'.length))
              : value === `monitor:${check.id}`,
          ),
      ),
    [checks, all, scope],
  );
  const sites = [...new Set(covered.map((check) => check.destination.siteId).filter(Boolean))];

  const change =
    <T,>(setter: (value: T) => void) =>
    (value: T) => {
      setter(value);
      setDirty(true);
    };
  const changeRecurring = (patch: Partial<MaintenanceSchedule>) => {
    setRecurring((current) => ({ ...current, ...patch }));
    setDirty(true);
  };

  const close = () => {
    if (!dirty) {
      onClose();
      return;
    }
    modal.confirm({
      title: 'Discard your unsaved changes to this window?',
      okText: 'Discard',
      okButtonProps: { danger: true },
      cancelText: 'Keep editing',
      onOk: onClose,
    });
  };

  const save = async () => {
    const request: MaintenanceRequest = {
      name,
      reason,
      enabled,
      scope: {
        all,
        folders: all
          ? []
          : scope.filter((value) => value.startsWith('folder:')).map((value) => value.slice(7)),
        monitorIds: all
          ? []
          : scope.filter((value) => value.startsWith('monitor:')).map((value) => value.slice(8)),
      },
      schedule,
      version: window?.version,
    };
    setSaving(true);
    try {
      const saved = window
        ? await maintenanceApi.update(window.id, request)
        : await maintenanceApi.create(request);
      message.success(`Saved "${saved.name}". Sites pick it up within 5 minutes.`);
      setDirty(false);
      await onSaved();
    } catch (reason) {
      message.error((reason as Error).message);
    } finally {
      setSaving(false);
    }
  };

  const weekdays = recurring.weekdays ?? [];
  const toggleDay = (day: Weekday) =>
    changeRecurring({
      weekdays: weekdays.includes(day)
        ? weekdays.filter((item) => item !== day)
        : [...weekdays, day],
    });
  const duration = recurring.durationMinutes ?? 60;

  return (
    <Drawer
      open
      onClose={close}
      size={560}
      title={window ? 'Edit maintenance window' : 'New maintenance window'}
      extra={
        <span className="inline-flex gap-2">
          <Button onClick={close}>Cancel</Button>
          <Button
            type="primary"
            loading={saving}
            onClick={save}
            disabled={!name.trim() || (!all && scope.length === 0) || !!preview?.error}
          >
            Save window
          </Button>
        </span>
      }
    >
      <div className="flex flex-col gap-4">
        <Labelled label="Name">
          <Input
            value={name}
            placeholder="Weekly DB patching"
            onChange={(event) => change(setName)(event.target.value)}
          />
        </Labelled>
        <Labelled label="Reason (shown on the monitors)">
          <Input
            value={reason}
            placeholder="Database minor version patching"
            onChange={(event) => change(setReason)(event.target.value)}
          />
        </Labelled>
        <Labelled
          label="Covers"
          hint={`${covered.length} monitor${covered.length === 1 ? '' : 's'}; a folder includes its subfolders.`}
        >
          <Select
            mode="multiple"
            disabled={all}
            value={scope}
            placeholder="Add monitors or folders"
            optionFilterProp="label"
            onChange={change(setScope)}
            options={[
              {
                label: 'Folders',
                options: folders.map((path) => ({
                  value: `folder:${path}`,
                  label: `Folder: ${path} (${checks.filter((check) => inFolder(check, path)).length})`,
                })),
              },
              {
                label: 'Monitors',
                options: checks.map((check) => ({
                  value: `monitor:${check.id}`,
                  label: check.folder ? `${check.name} (${check.folder})` : check.name,
                })),
              },
            ]}
          />
          <Checkbox checked={all} onChange={(event) => change(setAll)(event.target.checked)}>
            All monitors
          </Checkbox>
        </Labelled>
        <Labelled label="Schedule">
          <Segmented<Mode>
            className="self-start"
            value={mode}
            onChange={change(setMode)}
            options={[
              { value: 'once', label: 'One-off' },
              { value: 'recurring', label: 'Recurring' },
            ]}
          />
        </Labelled>
        {mode === 'once' ? (
          <div className="grid grid-cols-2 gap-3">
            <Labelled label="Start">
              <DatePicker
                showTime={{ format: 'HH:mm' }}
                format="YYYY-MM-DD HH:mm"
                value={once.start ? dayjs(once.start) : null}
                onChange={(value) =>
                  change(setOnce)({ ...once, start: value?.toISOString() ?? undefined })
                }
              />
            </Labelled>
            <Labelled label="End">
              <DatePicker
                showTime={{ format: 'HH:mm' }}
                format="YYYY-MM-DD HH:mm"
                value={once.end ? dayjs(once.end) : null}
                onChange={(value) =>
                  change(setOnce)({ ...once, end: value?.toISOString() ?? undefined })
                }
              />
            </Labelled>
          </div>
        ) : (
          <div className="flex flex-col gap-3">
            <div className="grid grid-cols-2 gap-3">
              <Labelled label="Repeats">
                <Select
                  value={recurring.kind}
                  options={REPEATS}
                  onChange={(kind) =>
                    changeRecurring({
                      kind,
                      weekdays:
                        kind === 'weekly' ? (weekdays.length ? weekdays : ['tue']) : undefined,
                      dayOfMonth: kind === 'monthly_day' ? (recurring.dayOfMonth ?? 1) : undefined,
                      weekOfMonth:
                        kind === 'monthly_weekday' ? (recurring.weekOfMonth ?? 1) : undefined,
                      weekday:
                        kind === 'monthly_weekday' ? (recurring.weekday ?? 'tue') : undefined,
                    })
                  }
                />
              </Labelled>
              <Labelled label="Time zone">
                <Select
                  showSearch
                  value={recurring.timeZone ?? 'UTC'}
                  options={zones.map((zone) => ({ value: zone, label: zone }))}
                  onChange={(timeZone) => changeRecurring({ timeZone })}
                />
              </Labelled>
            </div>
            {recurring.kind === 'weekly' && (
              <div className="flex flex-wrap gap-1" role="group" aria-label="Days">
                {WEEKDAYS.map((day) => (
                  <Button
                    key={day}
                    aria-pressed={weekdays.includes(day as Weekday)}
                    type={weekdays.includes(day as Weekday) ? 'primary' : 'default'}
                    ghost={weekdays.includes(day as Weekday)}
                    onClick={() => toggleDay(day as Weekday)}
                  >
                    {weekdayLabel(day)}
                  </Button>
                ))}
              </div>
            )}
            {recurring.kind === 'monthly_day' && (
              <Labelled label="Day of the month" hint="A shorter month uses its last day.">
                <InputNumber
                  min={1}
                  max={31}
                  value={recurring.dayOfMonth}
                  onChange={(value) => changeRecurring({ dayOfMonth: value ?? 1 })}
                />
              </Labelled>
            )}
            {recurring.kind === 'monthly_weekday' && (
              <div className="grid grid-cols-2 gap-3">
                <Labelled label="Week">
                  <Select
                    value={recurring.weekOfMonth}
                    options={WEEKS}
                    onChange={(weekOfMonth) => changeRecurring({ weekOfMonth })}
                  />
                </Labelled>
                <Labelled label="Weekday">
                  <Select
                    value={recurring.weekday}
                    options={WEEKDAYS.map((day) => ({ value: day, label: weekdayLabel(day) }))}
                    onChange={(weekday) => changeRecurring({ weekday: weekday as Weekday })}
                  />
                </Labelled>
              </div>
            )}
            <div className="grid grid-cols-3 gap-3">
              <Labelled label="Starts at">
                <Input
                  value={recurring.startTime}
                  placeholder="01:00"
                  onChange={(event) => changeRecurring({ startTime: event.target.value })}
                />
              </Labelled>
              <Labelled label="Lasts">
                <span className="inline-flex items-center gap-1">
                  <InputNumber
                    min={0}
                    max={168}
                    value={Math.floor(duration / 60)}
                    aria-label="Hours"
                    onChange={(hours) =>
                      changeRecurring({ durationMinutes: (hours ?? 0) * 60 + (duration % 60) })
                    }
                  />
                  <span className="text-xs text-gray-500">h</span>
                  <InputNumber
                    min={0}
                    max={59}
                    value={duration % 60}
                    aria-label="Minutes"
                    onChange={(minutes) =>
                      changeRecurring({
                        durationMinutes: Math.floor(duration / 60) * 60 + (minutes ?? 0),
                      })
                    }
                  />
                  <span className="text-xs text-gray-500">min</span>
                </span>
              </Labelled>
              <Labelled label="Until (optional)">
                <DatePicker
                  value={recurring.endDate ? dayjs(recurring.endDate) : null}
                  placeholder="No end"
                  onChange={(value) =>
                    changeRecurring({ endDate: value ? value.format('YYYY-MM-DD') : undefined })
                  }
                />
              </Labelled>
            </div>
          </div>
        )}
        <div className="flex items-center gap-2">
          <Switch checked={enabled} onChange={change(setEnabled)} aria-label="Enabled" />
          <span className="text-sm">{enabled ? 'Enabled' : 'Paused: no occurrences'}</span>
        </div>

        <div className="flex flex-col gap-1.5 rounded-md border border-slate-200 bg-slate-50 p-3 text-sm text-gray-700 dark:border-slate-700 dark:bg-slate-900/40 dark:text-gray-200">
          <strong className="text-gray-900 dark:text-gray-100">
            {mode === 'once' ? 'Occurrence' : 'Next occurrences'}
          </strong>
          {preview?.error && (
            <span className="text-red-600 dark:text-red-400">{preview.error}</span>
          )}
          {preview?.occurrences.map((occurrence) => (
            <span key={occurrence.start}>
              {occurrenceText(occurrence, mode === 'once' ? undefined : recurring.timeZone)}
            </span>
          ))}
          {!preview && <span className="text-gray-500">Choose when it starts and ends.</span>}
          <span className="text-gray-500 dark:text-gray-400">
            During the window runs continue, the monitors show Maintenance, uptime leaves it out,
            and alerts are muted
            {sites.length > 0
              ? ` on ${sites.join(', ')} (${covered.length} monitor${covered.length === 1 ? '' : 's'})`
              : ''}{' '}
            by managed silences.
          </span>
        </div>
      </div>
    </Drawer>
  );
};

const Labelled = ({
  label,
  hint,
  children,
}: {
  label: string;
  hint?: string;
  children: React.ReactNode;
}) => (
  <div className="flex flex-col gap-1">
    <span className="text-xs font-medium text-gray-600 dark:text-gray-300">{label}</span>
    {children}
    {hint && <span className="text-[11px] text-gray-500 dark:text-gray-400">{hint}</span>}
  </div>
);
