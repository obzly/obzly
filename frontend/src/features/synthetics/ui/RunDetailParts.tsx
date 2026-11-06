import {
  CheckCircleFilled,
  CloseCircleFilled,
  ExclamationCircleFilled,
  LoadingOutlined,
} from '@ant-design/icons';
import { Spin } from 'antd';
import { useEffect, useState } from 'react';

import {
  type RunResult,
  type StepResult,
  type StepStatus,
  type TestRun,
  syntheticsApi,
} from '../../../entity/synthetics';
import { formatMs } from '../lib/format';
import { PHASES } from '../lib/phases';
import { STATUS_COLOR } from '../lib/status';
import { StatusBadge } from './parts';

/** Where a run's time went, DNS to transfer, as offset bars. */
export const Waterfall = ({ run }: { run: RunResult }) => {
  const phases = PHASES.filter((phase) => (run.phasesMs?.[phase.key] ?? 0) > 0);
  const total = Math.max(
    run.durationMs,
    phases.reduce((sum, phase) => sum + (run.phasesMs?.[phase.key] ?? 0), 0),
    1,
  );
  let offset = 0;
  if (phases.length === 0) return null;
  return (
    <div className="flex flex-col gap-1.5 text-xs">
      {phases.map((phase) => {
        const ms = run.phasesMs?.[phase.key] ?? 0;
        const left = (offset / total) * 100;
        offset += ms;
        return (
          <div
            key={phase.key}
            className="grid grid-cols-[92px_minmax(0,1fr)_64px] items-center gap-2"
          >
            <span className="text-gray-500 dark:text-gray-400">{phase.label}</span>
            <div className="relative h-3 rounded bg-gray-100 dark:bg-gray-700">
              <span
                className="absolute top-0 bottom-0 rounded"
                style={{
                  left: `${left}%`,
                  width: `${Math.max(1.5, (ms / total) * 100)}%`,
                  background: phase.color,
                }}
              />
            </div>
            <span className="text-right tabular-nums">{formatMs(ms)}</span>
          </div>
        );
      })}
    </div>
  );
};

/** A step's phases as one stacked bar, DNS to transfer. */
const StepBar = ({ step }: { step: StepResult }) => {
  const phases = PHASES.filter((phase) => (step.phasesMs?.[phase.key] ?? 0) > 0);
  const total = phases.reduce((sum, phase) => sum + (step.phasesMs?.[phase.key] ?? 0), 0);
  if (total <= 0) return null;
  return (
    <div
      className="flex h-2 overflow-hidden rounded bg-gray-100 dark:bg-gray-700"
      role="img"
      aria-label={phases
        .map((phase) => `${phase.label} ${formatMs(step.phasesMs?.[phase.key])}`)
        .join(', ')}
    >
      {phases.map((phase) => (
        <span
          key={phase.key}
          style={{
            width: `${Math.max(2, ((step.phasesMs?.[phase.key] ?? 0) / total) * 100)}%`,
            background: phase.color,
          }}
        />
      ))}
    </div>
  );
};

const STEP_DOT: Record<StepStatus, string> = {
  up: STATUS_COLOR.up,
  warn: STATUS_COLOR.warn,
  down: STATUS_COLOR.down,
  skipped: '#9ca3af',
};

/** A multi-step run's steps: status, timing bar, assertions, extracted values (secrets masked). */
export const StepResults = ({ run }: { run: RunResult }) => {
  const steps = run.steps ?? [];
  const failed = steps.find((step) => step.status === 'down');
  return (
    <div className="flex flex-col gap-2">
      <div
        className="text-sm font-semibold"
        style={{ color: STATUS_COLOR[run.status === 'up' ? 'up' : run.status] }}
      >
        {failed
          ? `Failed at step ${failed.index} of ${steps.length}`
          : run.status === 'warn'
            ? `Passed with warnings · ${steps.length} steps`
            : `Passed · ${steps.length} of ${steps.length} steps`}{' '}
        · {formatMs(run.durationMs)}
      </div>
      {steps.map((step) => (
        <div
          key={step.index}
          className="flex flex-col gap-1.5 rounded border border-gray-200 p-2.5 dark:border-gray-700"
        >
          <div className="flex items-center gap-2 text-sm">
            <span
              className="inline-block h-2.5 w-2.5 shrink-0 rounded-full"
              style={{ background: STEP_DOT[step.status] }}
              role="img"
              aria-label={step.status}
            />
            <span className="font-semibold">
              {step.index}. {step.name}
            </span>
            <span className="ml-auto font-mono text-xs text-gray-500 dark:text-gray-400">
              {step.status === 'skipped'
                ? 'skipped'
                : !step.httpStatus && !step.durationMs
                  ? 'not sent'
                  : `${step.httpStatus ? `${step.httpStatus} · ` : ''}${formatMs(step.durationMs)}`}
            </span>
          </div>
          {step.status !== 'skipped' && (
            <>
              <div className="truncate font-mono text-[11px] text-gray-500 dark:text-gray-400">
                {step.method} {step.url}
              </div>
              <StepBar step={step} />
              {step.afterFailure && (
                <div className="text-xs text-gray-600 dark:text-gray-300">
                  Ran after an earlier failure (run even if an earlier step failed is on).
                </div>
              )}
              {step.error && (
                <div className="text-xs" style={{ color: STATUS_COLOR.down }}>
                  {step.error}
                </div>
              )}
              <Assertions run={{ assertions: step.assertions } as RunResult} onlyFailed />
              {step.assertions?.length && step.assertions.every((assertion) => assertion.passed) ? (
                <div className="text-xs text-gray-600 dark:text-gray-300">
                  {step.assertions.length === 1
                    ? 'The assertion passed'
                    : `All ${step.assertions.length} assertions passed`}
                </div>
              ) : null}
              {step.extracted?.length ? (
                <div className="font-mono text-xs text-gray-600 dark:text-gray-300">
                  {step.extracted
                    .map(
                      (value) =>
                        `${value.name} = ${value.secret ? '•••••••• (secret)' : (value.value ?? '')}`,
                    )
                    .join(' · ')}
                </div>
              ) : null}
              {step.bodySnippet && (
                <pre className="m-0 max-h-32 overflow-auto rounded bg-gray-50 p-2 font-mono text-[11px] whitespace-pre-wrap dark:bg-gray-900">
                  {step.bodySnippet}
                </pre>
              )}
            </>
          )}
        </div>
      ))}
      <div className="flex flex-wrap gap-3 text-[11px] text-gray-500 dark:text-gray-400">
        {PHASES.map((phase) => (
          <span key={phase.key} className="inline-flex items-center gap-1">
            <span className="inline-block h-2 w-2" style={{ background: phase.color }} />
            {phase.label}
          </span>
        ))}
      </div>
    </div>
  );
};

export const Assertions = ({ run, onlyFailed }: { run: RunResult; onlyFailed?: boolean }) => {
  const shown = (run.assertions ?? []).filter((assertion) => !onlyFailed || !assertion.passed);
  if (!shown.length) return null;
  return (
    <ul className="m-0 flex list-none flex-col gap-1 p-0 text-xs">
      {shown.map((assertion, index) => (
        <li key={index} className="flex items-baseline gap-2">
          {assertion.passed ? (
            <CheckCircleFilled style={{ color: STATUS_COLOR.up }} />
          ) : assertion.severity === 'warn' ? (
            <ExclamationCircleFilled style={{ color: STATUS_COLOR.warn }} />
          ) : (
            <CloseCircleFilled style={{ color: STATUS_COLOR.down }} />
          )}
          <span>
            {assertion.description}
            {!assertion.passed && assertion.actual && (
              <span className="text-gray-500 dark:text-gray-400"> (got {assertion.actual})</span>
            )}
          </span>
        </li>
      ))}
    </ul>
  );
};

/** Everything one run measured: verdict, timings, assertions, response and connection. */
export const RunDetail = ({ run }: { run: RunResult }) => (
  <div className="flex flex-col gap-4">
    <div className="flex flex-wrap items-center gap-2">
      <StatusBadge status={run.status} />
      <span className="font-mono text-xs">{run.location}</span>
      <span className="text-xs text-gray-500 dark:text-gray-400">
        {run.fromControlPlane ? 'from the control plane' : `probe ${run.probe}`} ·{' '}
        {new Date(run.startedAt).toLocaleString()} · {formatMs(run.durationMs)}
      </span>
    </div>
    {run.error && (
      <div
        className="rounded px-3 py-2 text-sm"
        style={{ background: `${STATUS_COLOR[run.status === 'warn' ? 'warn' : 'down']}1f` }}
      >
        {run.error}
      </div>
    )}
    {run.steps?.length ? (
      <Section title="Steps">
        <StepResults run={run} />
      </Section>
    ) : (
      <Section title="Timing">
        <Waterfall run={run} />
      </Section>
    )}
    {run.assertions?.length ? (
      <Section title="Assertions">
        <Assertions run={run} />
      </Section>
    ) : null}
    {run.httpStatus ? (
      <Section title="Response">
        <pre className="m-0 max-h-64 overflow-auto rounded bg-gray-50 p-3 font-mono text-xs whitespace-pre-wrap dark:bg-gray-900">
          {`HTTP ${run.httpStatus}${run.redirects ? ` after ${run.redirects} redirect(s)` : ''}\n`}
          {Object.entries(run.headers ?? {}).map(([name, value]) => `${name}: ${value}\n`)}
          {run.bodySnippet ? `\n${run.bodySnippet}` : ''}
        </pre>
      </Section>
    ) : null}
    {!run.steps?.length && (
      <Section title="Connection">
        <dl className="m-0 grid grid-cols-[auto_minmax(0,1fr)] gap-x-4 gap-y-1 text-xs">
          {run.resolvedIps?.length ? (
            <Row label="Resolved IPs" value={run.resolvedIps.join(', ')} />
          ) : null}
          {run.dnsAnswers?.length ? (
            <Row label="DNS answers" value={run.dnsAnswers.join(', ')} />
          ) : null}
          {run.tls && (
            <>
              <Row label="Certificate" value={`${run.tls.subject} (issuer ${run.tls.issuer})`} />
              <Row
                label="Expires"
                value={`${new Date(run.tls.notAfter).toLocaleDateString()} · ${Math.floor(run.tls.daysLeft)} days`}
              />
              <Row label="TLS" value={`${run.tls.protocol} · ${run.tls.cipher}`} />
              <Row
                label="Verification"
                value={
                  run.tls.verified ? 'valid' : `not valid: ${run.tls.verifyError ?? 'unknown'}`
                }
              />
            </>
          )}
          {run.icmp && (
            <Row
              label="ICMP"
              value={`${run.icmp.received}/${run.icmp.sent} replies · ${formatMs(run.icmp.rttAvgMs)} avg · ${formatMs(run.icmp.jitterMs)} jitter`}
            />
          )}
        </dl>
      </Section>
    )}
  </div>
);

const Section = ({ title, children }: { title: string; children: React.ReactNode }) => (
  <div>
    <div className="mb-1.5 text-sm font-semibold">{title}</div>
    {children}
  </div>
);

const Row = ({ label, value }: { label: string; value: string }) => (
  <>
    <dt className="text-gray-500 dark:text-gray-400">{label}</dt>
    <dd className="m-0 font-mono break-all">{value}</dd>
  </>
);

const POLL_MS = 1000;
const GIVE_UP_MS = 90_000;

/** A test run's per-location results, polled until every location has answered. */
export const TestResults = ({ run, onDone }: { run: TestRun; onDone?: (run: TestRun) => void }) => {
  const [current, setCurrent] = useState(run);
  const [timedOut, setTimedOut] = useState(false);

  useEffect(() => {
    setCurrent(run);
    setTimedOut(false);
    if (!run.tasks.some((task) => task.status === 'queued')) {
      onDone?.(run);
      return;
    }
    const started = Date.now();
    let stopped = false;
    const poll = async () => {
      while (!stopped) {
        await new Promise((resolve) => setTimeout(resolve, POLL_MS));
        if (stopped) return;
        try {
          const next = await syntheticsApi.getTest(run.id);
          setCurrent(next);
          if (!next.tasks.some((task) => task.status === 'queued')) {
            onDone?.(next);
            return;
          }
        } catch {
          /* keep polling until the give-up time */
        }
        if (Date.now() - started > GIVE_UP_MS) {
          setTimedOut(true);
          return;
        }
      }
    };
    void poll();
    return () => {
      stopped = true;
    };
  }, [run]);

  return (
    <div className="flex flex-col gap-3">
      {current.tasks.map((task) => (
        <div key={task.id} className="rounded-lg border border-gray-200 p-3 dark:border-gray-700">
          {task.result ? (
            <RunDetail run={task.result} />
          ) : (
            <div className="flex items-center justify-between gap-2">
              <span className="font-mono text-xs">{task.location}</span>
              <span className="flex items-center gap-2 text-xs text-gray-500 dark:text-gray-400">
                {task.status === 'queued' && !timedOut && (
                  <Spin size="small" indicator={<LoadingOutlined spin />} />
                )}
                {task.status === 'no_probe'
                  ? 'no probe online in this location'
                  : timedOut
                    ? 'the probe did not answer in time'
                    : `waiting for ${task.probe}`}
              </span>
            </div>
          )}
        </div>
      ))}
    </div>
  );
};
