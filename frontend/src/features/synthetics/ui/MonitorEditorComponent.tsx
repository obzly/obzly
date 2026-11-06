import { ArrowLeftOutlined, DeleteOutlined, PlusOutlined } from '@ant-design/icons';
import {
  App,
  AutoComplete,
  Button,
  Checkbox,
  Input,
  InputNumber,
  Select,
  Switch,
  Tag,
  Tooltip,
} from 'antd';
import { useMemo, useRef, useState } from 'react';

import type { SettingsChannel } from '../../../entity/alerting';
import {
  type AssertionKind,
  type CheckAlerts,
  type CheckRequest,
  type CheckType,
  type CheckView,
  type Definition,
  type Destination,
  type TestRun,
  syntheticsApi,
} from '../../../entity/synthetics';
import { folderPaths } from '../lib/folders';
import { cleanMultiDefinition, emptyStep } from '../lib/multiStep';
import { HTTP_ASSERTIONS, METHODS } from '../lib/requests';
import { MultiStepEditor, VariablesPanel } from './MultiStepEditor';
import { TestResults } from './RunDetailParts';
import { AssertionsEditor, Field } from './editorParts';
import { Panel } from './parts';

interface Props {
  check?: CheckView;
  destinations: Destination[];
  locations: string[];
  /** Every monitor, for the folder suggestions and Depends on. */
  checks: CheckView[];
  /** Obzly settings' central channels, for alerts. */
  channels: SettingsChannel[];
  onSaved: (check: CheckView) => void;
  onClose: () => void;
}

const TYPES: { value: CheckType; label: string }[] = [
  { value: 'http', label: 'HTTP / API' },
  { value: 'multi', label: 'Multi-step API' },
  { value: 'tls', label: 'TLS certificate' },
  { value: 'tcp', label: 'TCP port' },
  { value: 'dns', label: 'DNS' },
  { value: 'icmp', label: 'ICMP (ping)' },
];

const INTERVALS = [30, 60, 120, 180, 300, 600, 900, 1800, 3600];
const RECORD_TYPES = ['A', 'AAAA', 'CNAME', 'MX', 'TXT', 'NS'];
const REPEATS = [
  { value: 0, label: 'Never' },
  { value: 900, label: 'Every 15 min' },
  { value: 3600, label: 'Every hour' },
  { value: 4 * 3600, label: 'Every 4 h' },
  { value: 86400, label: 'Every day' },
];
const NO_ALERTS: CheckAlerts = { channelIds: [], notify: 'down', certDays: 0, repeatSeconds: 0 };

const PLACEHOLDER = /\$\{env:([A-Za-z_][A-Za-z0-9_]*)\}/g;

const emptyDefinition = (type: CheckType): Definition => {
  switch (type) {
    case 'http':
      return {
        type,
        timeoutSeconds: 10,
        request: { method: 'GET', url: 'https://', followRedirects: true },
        assertions: [{ kind: 'status', op: 'equals', value: '200', severity: 'fail' }],
      };
    case 'tls':
      return { type, timeoutSeconds: 10, target: '', tls: { warnDays: 21 } };
    case 'dns':
      return { type, timeoutSeconds: 10, target: '', dns: { recordType: 'A' } };
    case 'icmp':
      return { type, timeoutSeconds: 10, target: '', icmp: { count: 5, maxLossRatio: 0 } };
    case 'multi':
      return { type, timeoutSeconds: 60, steps: [emptyStep(1)], variables: {} };
    default:
      return { type, timeoutSeconds: 10, target: '' };
  }
};

const requestFrom = (check: CheckView | undefined, destinations: Destination[]): CheckRequest =>
  check
    ? {
        name: check.name,
        enabled: check.enabled,
        intervalSeconds: check.intervalSeconds,
        downLocations: check.downLocations,
        locations: check.locations,
        destinationId: check.destination.id,
        definition: structuredClone(check.definition),
        folder: check.folder ?? '',
        parentIds: [...(check.parentIds ?? [])],
        alerts: { ...NO_ALERTS, ...check.alerts, notify: check.alerts?.notify || 'down' },
        version: check.version,
      }
    : {
        name: '',
        enabled: true,
        intervalSeconds: 180,
        downLocations: 0,
        locations: [],
        destinationId: destinations.length === 1 ? destinations[0].id : '',
        definition: emptyDefinition('http'),
        folder: '',
        parentIds: [],
        alerts: NO_ALERTS,
      };

const placeholdersOf = (definition: Definition): string[] => {
  const texts = [
    definition.target ?? '',
    definition.request?.url ?? '',
    definition.request?.body ?? '',
    ...Object.values(definition.request?.headers ?? {}),
    ...(definition.steps ?? []).flatMap((step) => [
      step.request.url,
      step.request.body ?? '',
      ...Object.values(step.request.headers ?? {}),
    ]),
    ...Object.values(definition.variables ?? {}),
  ];
  return [
    ...new Set(texts.flatMap((text) => [...text.matchAll(PLACEHOLDER)].map((match) => match[1]))),
  ];
};

export const MonitorEditorComponent = ({
  check,
  destinations,
  locations,
  checks,
  channels,
  onSaved,
  onClose,
}: Props) => {
  const { message, modal } = App.useApp();
  const [draft, setDraft] = useState<CheckRequest>(() => requestFrom(check, destinations));
  const [secrets, setSecrets] = useState<Record<string, string>>({});
  const [headers, setHeaders] = useState<[string, string][]>(() =>
    Object.entries(check?.definition.request?.headers ?? {}),
  );
  const [dirty, setDirty] = useState(false);
  const [saving, setSaving] = useState(false);
  const [testRun, setTestRun] = useState<TestRun>();
  const [testLocations, setTestLocations] = useState<string[]>([]);
  const [fromControlPlane, setFromControlPlane] = useState(false);
  const [testing, setTesting] = useState(false);
  // Test & save: save once the test comes back green.
  const pendingSave = useRef(false);

  const update = (change: Partial<CheckRequest>) => {
    setDraft((current) => ({ ...current, ...change }));
    setDirty(true);
  };
  const updateAlerts = (change: Partial<CheckAlerts>) =>
    update({ alerts: { ...(draft.alerts ?? NO_ALERTS), ...change } });
  const updateDefinition = (change: Partial<Definition>) =>
    update({ definition: { ...draft.definition, ...change } });
  const updateRequest = (change: Partial<NonNullable<Definition['request']>>) =>
    updateDefinition({ request: { ...draft.definition.request!, ...change } });
  const setHeaderRows = (rows: [string, string][]) => {
    setHeaders(rows);
    const map = Object.fromEntries(rows.filter(([name]) => name.trim()));
    updateRequest({ headers: Object.keys(map).length ? map : undefined });
  };

  const definition = draft.definition;
  const placeholders = useMemo(() => placeholdersOf(definition), [definition]);
  const stored = new Map((check?.secrets ?? []).map((secret) => [secret.name, secret.stored]));
  const alerts = draft.alerts ?? NO_ALERTS;
  const folders = useMemo(() => folderPaths(checks), [checks]);
  // A monitor can't depend on itself or on one that already depends on it.
  const parentOptions = useMemo(() => {
    const below = new Set<string>();
    if (check) {
      const queue = [check.id];
      while (queue.length > 0) {
        const id = queue.shift()!;
        for (const other of checks) {
          if (!below.has(other.id) && (other.parentIds ?? []).includes(id)) {
            below.add(other.id);
            queue.push(other.id);
          }
        }
      }
    }
    return checks
      .filter((other) => other.id !== check?.id)
      .map((other) => ({
        value: other.id,
        label: other.folder ? `${other.name} (${other.folder})` : other.name,
        disabled: below.has(other.id),
        title: below.has(other.id) ? 'Depends on this monitor: that would be a loop' : undefined,
      }));
  }, [checks, check]);

  const payload = (): CheckRequest => ({
    ...draft,
    definition: cleanMultiDefinition(draft.definition),
    secrets: Object.fromEntries(
      Object.entries(secrets).filter(([name, value]) => value && placeholders.includes(name)),
    ),
  });

  const leave = () => {
    if (!dirty) {
      onClose();
      return;
    }
    modal.confirm({
      title: 'Discard your unsaved changes to this monitor?',
      okText: 'Discard',
      okButtonProps: { danger: true },
      cancelText: 'Keep editing',
      onOk: onClose,
    });
  };

  const save = async () => {
    setSaving(true);
    try {
      const saved = check
        ? await syntheticsApi.updateCheck(check.id, payload())
        : await syntheticsApi.createCheck(payload());
      message.success(`Saved "${saved.name}". Probes pick it up on their next config pull.`);
      setDirty(false);
      onSaved(saved);
    } catch (reason) {
      message.error((reason as Error).message);
    } finally {
      setSaving(false);
    }
  };

  const test = async (thenSave: boolean) => {
    setTesting(true);
    try {
      const run = await syntheticsApi.createTest({
        checkId: check?.id,
        check: payload(),
        locations: fromControlPlane ? undefined : testLocations.length ? testLocations : undefined,
        fromControlPlane,
      });
      setTestRun(run);
      if (thenSave) pendingSave.current = true;
    } catch (reason) {
      message.error((reason as Error).message);
    } finally {
      setTesting(false);
    }
  };

  const onTestDone = (run: TestRun) => {
    if (!pendingSave.current) return;
    pendingSave.current = false;
    const failed = run.tasks.filter(
      (task) => task.status !== 'done' || task.result?.status === 'down',
    );
    if (failed.length === 0) {
      void save();
      return;
    }
    modal.confirm({
      title: 'The test failed',
      content: `It didn't pass from ${failed.map((task) => task.location).join(', ')}. Save anyway?`,
      okText: 'Save anyway',
      cancelText: 'Keep editing',
      onOk: save,
    });
  };

  const assertionKinds: AssertionKind[] =
    definition.type === 'http' ? HTTP_ASSERTIONS : ['response_time_ms'];
  const multi = definition.type === 'multi';
  const hasBody = ['POST', 'PUT', 'PATCH', 'DELETE'].includes(definition.request?.method ?? '');

  return (
    <div className="flex flex-col gap-4">
      <div className="flex flex-wrap items-center gap-3 border-b border-gray-200 pb-3 dark:border-gray-700">
        <Button type="text" icon={<ArrowLeftOutlined />} onClick={leave}>
          Monitors
        </Button>
        <span className="text-gray-500 dark:text-gray-400">
          / <b className="text-gray-900 dark:text-gray-100">{check?.name ?? 'New monitor'}</b>{' '}
          {check && '/ Edit'}
        </span>
        {dirty && <Tag color="warning">Unsaved changes</Tag>}
        <span className="ml-auto text-xs text-gray-500 dark:text-gray-400">
          {check
            ? `Version ${check.version} · saved ${new Date(check.updatedAt).toLocaleString()}`
            : 'Not saved yet'}
        </span>
      </div>

      <div className="grid gap-4 xl:grid-cols-[minmax(0,1fr)_minmax(0,1fr)]">
        <Panel
          title={check ? 'Edit monitor' : 'New monitor'}
          extra={
            <label className="flex items-center gap-2 text-sm">
              <Switch
                size="small"
                checked={draft.enabled}
                onChange={(enabled) => update({ enabled })}
              />{' '}
              Enabled
            </label>
          }
        >
          <div className="flex flex-col gap-4">
            <div className="grid gap-3 sm:grid-cols-[minmax(0,1fr)_200px]">
              <Field label="Name">
                <Input
                  value={draft.name}
                  onChange={(event) => update({ name: event.target.value })}
                  placeholder="Public API health"
                />
              </Field>
              <Field label="Type">
                <Select
                  value={definition.type}
                  options={TYPES}
                  onChange={(type: CheckType) => {
                    setHeaders([]);
                    update({ definition: emptyDefinition(type) });
                  }}
                />
              </Field>
            </div>

            {definition.type === 'http' && definition.request && (
              <>
                <div className="grid gap-3 sm:grid-cols-[120px_minmax(0,1fr)]">
                  <Field label="Method">
                    <Select
                      value={definition.request.method}
                      options={METHODS.map((method) => ({ value: method, label: method }))}
                      onChange={(method) => updateRequest({ method })}
                    />
                  </Field>
                  <Field label="URL">
                    <Input
                      className="font-mono"
                      value={definition.request.url}
                      onChange={(event) => updateRequest({ url: event.target.value })}
                    />
                  </Field>
                </div>
                <Field
                  label="Headers"
                  hint="Use ${env:NAME} for secrets: values are stored encrypted, below."
                >
                  <div className="flex flex-col gap-2">
                    {headers.map(([name, value], index) => (
                      <div
                        key={index}
                        className="grid grid-cols-[minmax(0,2fr)_minmax(0,3fr)_32px] gap-2"
                      >
                        <Input
                          placeholder="Authorization"
                          value={name}
                          onChange={(event) =>
                            setHeaderRows(
                              headers.map((row, i) =>
                                i === index ? [event.target.value, row[1]] : row,
                              ),
                            )
                          }
                        />
                        <Input
                          className="font-mono"
                          placeholder="Bearer ${env:API_TOKEN}"
                          value={value}
                          onChange={(event) =>
                            setHeaderRows(
                              headers.map((row, i) =>
                                i === index ? [row[0], event.target.value] : row,
                              ),
                            )
                          }
                        />
                        <Button
                          type="text"
                          icon={<DeleteOutlined />}
                          aria-label="Remove header"
                          onClick={() => setHeaderRows(headers.filter((_, i) => i !== index))}
                        />
                      </div>
                    ))}
                    <Button
                      className="self-start"
                      size="small"
                      icon={<PlusOutlined />}
                      onClick={() => setHeaders([...headers, ['', '']])}
                    >
                      Add header
                    </Button>
                  </div>
                </Field>
                {hasBody && (
                  <Field label="Body">
                    <Input.TextArea
                      className="font-mono"
                      rows={4}
                      value={definition.request.body ?? ''}
                      onChange={(event) => updateRequest({ body: event.target.value || undefined })}
                    />
                  </Field>
                )}
                <div className="flex flex-wrap gap-6">
                  <label className="flex items-center gap-2 text-sm">
                    <Switch
                      size="small"
                      checked={definition.request.followRedirects !== false}
                      onChange={(followRedirects) => updateRequest({ followRedirects })}
                    />{' '}
                    Follow redirects
                  </label>
                  <TLSVerify definition={definition} onChange={updateDefinition} />
                </div>
              </>
            )}

            {definition.type !== 'http' && definition.type !== 'multi' && (
              <Field
                label={
                  {
                    tls: 'Target (host:port)',
                    tcp: 'Target (host:port)',
                    dns: 'Name to resolve',
                    icmp: 'Host or IP',
                  }[definition.type]
                }
              >
                <Input
                  className="font-mono"
                  value={definition.target ?? ''}
                  onChange={(event) => updateDefinition({ target: event.target.value })}
                  placeholder={
                    definition.type === 'dns'
                      ? 'example.com'
                      : definition.type === 'icmp'
                        ? '10.20.0.1'
                        : 'db.internal:5432'
                  }
                />
              </Field>
            )}

            {definition.type === 'tls' && (
              <div className="flex flex-col gap-3">
                <div className="flex flex-wrap gap-6">
                  <TLSVerify definition={definition} onChange={updateDefinition} />
                  <label className="flex items-center gap-2 text-sm">
                    <Switch
                      size="small"
                      checked={definition.tls?.verifyHostname !== false}
                      onChange={(verifyHostname) =>
                        updateDefinition({ tls: { ...definition.tls, verifyHostname } })
                      }
                    />{' '}
                    Check the hostname
                  </label>
                </div>
                <div className="grid gap-3 sm:grid-cols-3">
                  <Field label="Fail below (days)">
                    <InputNumber
                      className="w-full"
                      min={0}
                      value={definition.tls?.failDays ?? 0}
                      onChange={(failDays) =>
                        updateDefinition({ tls: { ...definition.tls, failDays: failDays ?? 0 } })
                      }
                    />
                  </Field>
                  <Field label="Minimum protocol">
                    <Select
                      allowClear
                      value={definition.tls?.minProtocol || undefined}
                      placeholder="Any"
                      options={['TLS1.0', 'TLS1.1', 'TLS1.2', 'TLS1.3'].map((value) => ({
                        value,
                        label: value,
                      }))}
                      onChange={(minProtocol) =>
                        updateDefinition({ tls: { ...definition.tls, minProtocol } })
                      }
                    />
                  </Field>
                </div>
              </div>
            )}

            {definition.type === 'dns' && (
              <div className="grid gap-3 sm:grid-cols-3">
                <Field label="Record type">
                  <Select
                    value={definition.dns?.recordType ?? 'A'}
                    options={RECORD_TYPES.map((value) => ({ value, label: value }))}
                    onChange={(recordType) =>
                      updateDefinition({ dns: { ...definition.dns, recordType } })
                    }
                  />
                </Field>
                <Field label="Resolver" hint="Empty: the probe's own">
                  <Input
                    className="font-mono"
                    placeholder="1.1.1.1:53"
                    value={definition.dns?.resolver ?? ''}
                    onChange={(event) =>
                      updateDefinition({
                        dns: { ...definition.dns, resolver: event.target.value || undefined },
                      })
                    }
                  />
                </Field>
                <Field label="Expect any of">
                  <Select
                    mode="tags"
                    tokenSeparators={[',', ' ']}
                    value={definition.dns?.expectAnyOf ?? []}
                    onChange={(expectAnyOf: string[]) =>
                      updateDefinition({ dns: { ...definition.dns, expectAnyOf } })
                    }
                  />
                </Field>
              </div>
            )}

            {definition.type === 'icmp' && (
              <div className="grid gap-3 sm:grid-cols-3">
                <Field label="Pings">
                  <InputNumber
                    className="w-full"
                    min={1}
                    max={20}
                    value={definition.icmp?.count ?? 5}
                    onChange={(count) =>
                      updateDefinition({ icmp: { ...definition.icmp, count: count ?? 5 } })
                    }
                  />
                </Field>
                <Field label="Allowed loss (%)">
                  <InputNumber
                    className="w-full"
                    min={0}
                    max={100}
                    value={Math.round((definition.icmp?.maxLossRatio ?? 0) * 100)}
                    onChange={(loss) =>
                      updateDefinition({
                        icmp: { ...definition.icmp, maxLossRatio: (loss ?? 0) / 100 },
                      })
                    }
                  />
                </Field>
                <Field label="Warn above (ms)">
                  <InputNumber
                    className="w-full"
                    min={0}
                    value={definition.icmp?.rttWarnMs ?? 0}
                    onChange={(rttWarnMs) =>
                      updateDefinition({ icmp: { ...definition.icmp, rttWarnMs: rttWarnMs ?? 0 } })
                    }
                  />
                </Field>
              </div>
            )}

            {definition.type === 'multi' && (
              <>
                <TLSVerify definition={definition} onChange={updateDefinition} />
                <MultiStepEditor definition={definition} onChange={updateDefinition} />
              </>
            )}

            {definition.type !== 'multi' && (
              <Field
                label="Assertions"
                hint="A warn assertion makes a failure amber instead of red."
              >
                <AssertionsEditor
                  assertions={definition.assertions ?? []}
                  kinds={assertionKinds}
                  onChange={(assertions) => updateDefinition({ assertions })}
                />
              </Field>
            )}

            <div className="grid gap-3 sm:grid-cols-3">
              <Field label="Interval">
                <Select
                  value={draft.intervalSeconds}
                  options={INTERVALS.map((seconds) => ({
                    value: seconds,
                    label:
                      seconds < 60
                        ? `${seconds} s`
                        : seconds < 3600
                          ? `${seconds / 60} min${seconds === 180 ? ' (default)' : ''}`
                          : `${seconds / 3600} h`,
                  }))}
                  onChange={(intervalSeconds) => update({ intervalSeconds })}
                />
              </Field>
              <Field
                label={multi ? 'Total timeout (s)' : 'Timeout (s)'}
                hint={multi ? 'The whole run, at most 120 s' : undefined}
              >
                <InputNumber
                  className="w-full"
                  min={1}
                  max={multi ? 120 : 60}
                  value={definition.timeoutSeconds}
                  onChange={(timeoutSeconds) =>
                    updateDefinition({ timeoutSeconds: timeoutSeconds ?? (multi ? 60 : 10) })
                  }
                />
              </Field>
              <Field label="Red when down from" hint="0: half the locations">
                <InputNumber
                  className="w-full"
                  min={0}
                  value={draft.downLocations}
                  suffix="locations"
                  onChange={(downLocations) => update({ downLocations: downLocations ?? 0 })}
                />
              </Field>
            </div>
            <div className="grid gap-3 sm:grid-cols-2">
              <Field label="Locations" hint="Empty: every location">
                <Select
                  mode="tags"
                  value={draft.locations}
                  placeholder="All locations"
                  options={locations.map((location) => ({ value: location, label: location }))}
                  onChange={(next: string[]) => update({ locations: next })}
                />
              </Field>
              <Field label="Results go to">
                <Select
                  value={draft.destinationId || undefined}
                  placeholder="Choose a destination"
                  options={destinations.map((destination) => ({
                    value: destination.id,
                    label: `${destination.name} (${destination.siteId || 'site'})`,
                  }))}
                  onChange={(destinationId) => update({ destinationId })}
                />
              </Field>
            </div>
            <div className="grid gap-3 sm:grid-cols-2">
              <Field label="Folder" hint="Nest with /, e.g. Payments/EU. Empty: top level">
                <AutoComplete
                  value={draft.folder ?? ''}
                  placeholder="No folder"
                  options={folders
                    .filter((path) =>
                      path.toLowerCase().includes((draft.folder ?? '').trim().toLowerCase()),
                    )
                    .map((path) => ({ value: path }))}
                  onChange={(folder: string) => update({ folder })}
                />
              </Field>
              <Field
                label="Depends on"
                hint="While any of these is down this monitor shows Impacted and its alerts are muted"
              >
                <Select
                  mode="multiple"
                  value={draft.parentIds ?? []}
                  placeholder="No dependencies"
                  optionFilterProp="label"
                  options={parentOptions}
                  onChange={(parentIds: string[]) => update({ parentIds })}
                />
              </Field>
            </div>

            <Field
              label="Alerts"
              hint={
                channels.length === 0
                  ? 'Add a notification channel in Obzly settings to send alerts.'
                  : 'Rules are kept on the Site the results go to and shown read-only in Alerts. Muting by a parent needs both in the same destination project.'
              }
            >
              <div className="grid gap-3 sm:grid-cols-[minmax(0,2fr)_minmax(0,1fr)_minmax(0,1fr)_minmax(0,1fr)]">
                <Select
                  mode="multiple"
                  aria-label="Notification channels"
                  value={alerts.channelIds}
                  placeholder="No alerts"
                  optionFilterProp="label"
                  disabled={channels.length === 0}
                  options={channels.map((channel) => ({
                    value: channel.id,
                    label: `${channel.name} (${channel.kind})`,
                  }))}
                  onChange={(channelIds: string[]) => updateAlerts({ channelIds })}
                />
                <Select
                  aria-label="Notify on"
                  value={alerts.notify || 'down'}
                  disabled={alerts.channelIds.length === 0}
                  options={[
                    { value: 'down', label: 'When down' },
                    { value: 'degraded', label: 'When down or degraded' },
                  ]}
                  onChange={(notify) => updateAlerts({ notify })}
                />
                <InputNumber
                  aria-label="Certificate alert days"
                  className="w-full"
                  min={0}
                  max={365}
                  value={alerts.certDays}
                  disabled={alerts.channelIds.length === 0}
                  prefix="Cert"
                  suffix="days"
                  onChange={(certDays) => updateAlerts({ certDays: certDays ?? 0 })}
                />
                <Select
                  aria-label="Repeat"
                  value={alerts.repeatSeconds}
                  disabled={alerts.channelIds.length === 0}
                  options={REPEATS.map((repeat) => ({
                    ...repeat,
                    label: repeat.value === 0 ? 'Repeat: never' : repeat.label,
                  }))}
                  onChange={(repeatSeconds) => updateAlerts({ repeatSeconds })}
                />
              </div>
            </Field>

            {definition.type === 'http' && definition.request?.url.startsWith('https') && (
              <Field label="Certificate warning (days)">
                <InputNumber
                  min={1}
                  value={definition.tls?.warnDays ?? 21}
                  onChange={(warnDays) =>
                    updateDefinition({ tls: { ...definition.tls, warnDays: warnDays ?? 21 } })
                  }
                />
              </Field>
            )}

            {placeholders.length > 0 && (
              <Field
                label="Secrets"
                hint="Stored encrypted and sent only to probes; never shown again. Leave empty to keep the stored value, or to let the probe's environment provide it."
              >
                <div className="flex flex-col gap-2">
                  {placeholders.map((name) => (
                    <div
                      key={name}
                      className="grid grid-cols-[minmax(0,1fr)_minmax(0,2fr)] items-center gap-2"
                    >
                      <span className="font-mono text-xs">{name}</span>
                      <Input.Password
                        autoComplete="new-password"
                        placeholder={
                          stored.get(name)
                            ? 'stored (type to replace)'
                            : 'from the probe environment'
                        }
                        value={secrets[name] ?? ''}
                        onChange={(event) => {
                          setSecrets({ ...secrets, [name]: event.target.value });
                          setDirty(true);
                        }}
                      />
                    </div>
                  ))}
                </div>
              </Field>
            )}

            <div className="flex flex-wrap justify-end gap-2 border-t border-gray-200 pt-3 dark:border-gray-700">
              <Button type="text" onClick={leave}>
                Cancel
              </Button>
              <Button onClick={() => test(false)} loading={testing}>
                Test
              </Button>
              <Button onClick={() => test(true)} loading={testing || saving}>
                Test &amp; save
              </Button>
              <Button type="primary" onClick={save} loading={saving}>
                Save
              </Button>
            </div>
          </div>
        </Panel>

        <div className="flex min-w-0 flex-col gap-4 self-start">
          {multi && <VariablesPanel definition={definition} onChange={updateDefinition} />}
          <Panel
            title="Test result"
            subtitle={
              multi
                ? 'Runs the whole sequence once from each location; nothing is sent to the Site'
                : 'Runs this definition once, saved or not; nothing is sent to the Site'
            }
          >
            <div className="mb-3 flex flex-wrap items-center gap-3">
              <Select
                mode="multiple"
                className="min-w-[220px] flex-1"
                placeholder="Every location with a probe online"
                disabled={fromControlPlane}
                value={testLocations}
                options={locations.map((location) => ({ value: location, label: location }))}
                onChange={setTestLocations}
              />
              <Tooltip title="For targets no probe can reach. Runs from the control plane, which can't reach loopback or cloud metadata.">
                <Checkbox
                  checked={fromControlPlane}
                  onChange={(event) => setFromControlPlane(event.target.checked)}
                >
                  From the control plane
                </Checkbox>
              </Tooltip>
            </div>
            {testRun ? (
              <TestResults run={testRun} onDone={onTestDone} />
            ) : (
              <p className="text-sm text-gray-500 dark:text-gray-400">
                Press <b>Test</b> to run the monitor once. Results come back within seconds through
                each probe&apos;s open connection.
              </p>
            )}
          </Panel>
        </div>
      </div>
    </div>
  );
};

const TLSVerify = ({
  definition,
  onChange,
}: {
  definition: Definition;
  onChange: (change: Partial<Definition>) => void;
}) => (
  <Tooltip title="Off accepts self-signed, expired or untrusted certificates (internal services). Runs are labelled tls_verified=false.">
    <label className="flex items-center gap-2 text-sm">
      <Switch
        size="small"
        checked={definition.tls?.verify !== false}
        onChange={(verify) => onChange({ tls: { ...definition.tls, verify } })}
      />{' '}
      Verify TLS certificate
    </label>
  </Tooltip>
);
