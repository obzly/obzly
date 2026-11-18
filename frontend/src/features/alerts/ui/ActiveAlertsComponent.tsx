import { CheckCircleOutlined, LoadingOutlined, ReloadOutlined } from '@ant-design/icons';
import { App, Button, Spin, Table } from 'antd';
import dayjs from 'dayjs';
import { useEffect, useState } from 'react';

import {
  type AlertRule,
  type AlertView,
  type Matcher,
  alertingApi,
} from '../../../entity/alerting';
import { useVisibleInterval } from '../../../shared/lib';
import { type StatusColors, useTheme } from '../../../shared/theme';
import { Card } from '../../../shared/ui/v2';
import { Pill, type PillTone } from './bits';
import { LabelTags } from './labels';
import { errorMessage } from './lib';

const STATE_TONES: Record<AlertView['state'], PillTone> = {
  firing: 'bad',
  pending: 'warn',
  resolved: 'ok',
};

// Status colours: resolved stays green in every theme.
const stateColor = (status: StatusColors, state: AlertView['state']): string =>
  ({ firing: status.bad, pending: status.warn, resolved: status.ok })[state];

const formatValue = (value: number) => Number(value.toPrecision(6));

/** How far the value is toward an upper threshold; null where a bar would mislead. */
const thresholdRatio = (value: number, rule?: AlertRule): number | null => {
  if (!rule || rule.threshold <= 0 || (rule.comparison !== '>' && rule.comparison !== '>=')) {
    return null;
  }
  return Math.min(Math.max(value / rule.threshold, 0), 1);
};

/** Silence exactly this series: every label of it must match. */
const matchersFor = (alert: AlertView): Matcher[] =>
  Object.entries({ alertname: alert.ruleName, ...alert.labels }).map(([name, value]) => ({
    name,
    value,
  }));

interface Props {
  projectId: string;
  /** Set when the user may create silences. */
  onSilence?: (matchers: Matcher[]) => void;
}

export const ActiveAlertsComponent = ({ projectId, onSilence }: Props) => {
  const { message } = App.useApp();
  const { status } = useTheme();
  const [alerts, setAlerts] = useState<AlertView[]>([]);
  // Rules give the firing cards their threshold; the list still works without them.
  const [rules, setRules] = useState<Map<string, AlertRule>>(new Map());
  const [isLoading, setIsLoading] = useState(true);

  const load = async () => {
    setIsLoading(true);
    try {
      const [loadedAlerts, loadedRules] = await Promise.all([
        alertingApi.listAlerts(projectId),
        alertingApi.listRules(projectId).catch(() => null),
      ]);
      setAlerts(loadedAlerts);
      if (loadedRules) setRules(new Map(loadedRules.map((rule) => [rule.id, rule])));
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

  const live = alerts
    .filter((alert) => alert.state !== 'resolved')
    .sort((a, b) => (a.state === b.state ? 0 : a.state === 'firing' ? -1 : 1));

  return (
    <div className="flex flex-col gap-5">
      <section aria-labelledby="alerts-firing" className="flex flex-col gap-3">
        <div className="flex flex-wrap items-center justify-between gap-2">
          <h2
            id="alerts-firing"
            className="m-0 text-[17px] font-semibold text-gray-900 dark:text-gray-50"
          >
            Firing and pending
          </h2>
          <span className="flex flex-wrap items-center gap-2.5">
            <span className="text-xs text-gray-500 dark:text-gray-400">
              Auto-refreshes every 30 s while this page is open
            </span>
            <Button icon={<ReloadOutlined />} onClick={load} loading={isLoading}>
              Refresh
            </Button>
          </span>
        </div>
        {live.length === 0 ? (
          <div className="flex items-center gap-3 rounded-2xl border border-gray-200 bg-white p-5 text-sm text-gray-600 dark:border-gray-700 dark:bg-gray-800 dark:text-gray-300">
            <CheckCircleOutlined className="text-lg text-emerald-600 dark:text-emerald-500" />
            All clear: no rule is pending or firing.
          </div>
        ) : (
          <div className="grid grid-cols-[repeat(auto-fill,minmax(280px,1fr))] gap-4">
            {live.map((alert) => {
              const rule = rules.get(alert.ruleId);
              const ratio = thresholdRatio(alert.value, rule);
              const color = stateColor(status, alert.state);
              const since = alert.state === 'firing' ? alert.firedAt : alert.activeAt;
              return (
                <article
                  key={alert.ruleId + alert.fingerprint}
                  className="flex min-w-0 flex-col gap-2.5 rounded-2xl border border-gray-200 bg-white p-5 dark:border-gray-700 dark:bg-gray-800"
                  style={{ borderLeft: `4px solid ${color}` }}
                >
                  <span className="flex items-start justify-between gap-2">
                    <strong className="min-w-0 font-semibold break-words text-gray-900 dark:text-gray-50">
                      {alert.ruleName}
                    </strong>
                    <Pill tone={STATE_TONES[alert.state]}>{alert.state}</Pill>
                  </span>
                  <span className="flex flex-wrap items-baseline gap-2">
                    <span className="font-mono text-[26px] leading-none font-semibold text-gray-900 dark:text-gray-50">
                      {formatValue(alert.value)}
                    </span>
                    <span className="text-xs text-gray-500 dark:text-gray-400">
                      {rule ? `threshold ${rule.comparison} ${rule.threshold}` : 'threshold n/a'}
                    </span>
                  </span>
                  {ratio !== null && (
                    <span
                      role="img"
                      aria-label={`${Math.round(ratio * 100)}% of the threshold`}
                      className="block h-1.5 overflow-hidden rounded-full bg-[var(--v2-track)]"
                    >
                      <span
                        className="block h-full rounded-full"
                        style={{ width: `${Math.max(ratio * 100, 2)}%`, background: color }}
                      />
                    </span>
                  )}
                  <LabelTags labels={alert.labels} hide={['alertname']} />
                  <span className="mt-auto flex items-center justify-between gap-2">
                    <span className="text-xs text-gray-500 dark:text-gray-400">
                      since {dayjs(since ?? alert.activeAt).fromNow()}
                    </span>
                    {onSilence && (
                      <Button size="small" onClick={() => onSilence(matchersFor(alert))}>
                        Silence
                      </Button>
                    )}
                  </span>
                </article>
              );
            })}
          </div>
        )}
      </section>

      <Card
        title="All active alerts"
        subtitle="Pending, firing and recently resolved, one row per series"
      >
        <Table
          rowKey={(alert) => alert.ruleId + alert.fingerprint}
          dataSource={alerts}
          pagination={false}
          scroll={{ x: 'max-content' }}
          locale={{ emptyText: 'Nothing pending or firing' }}
          columns={[
            {
              title: 'State',
              dataIndex: 'state',
              width: 110,
              render: (state: AlertView['state']) => <Pill tone={STATE_TONES[state]}>{state}</Pill>,
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
              render: (value: number) => <span className="font-mono">{formatValue(value)}</span>,
            },
            {
              title: 'Since',
              width: 160,
              render: (_, alert) => {
                const at = alert.state === 'resolved' ? alert.resolvedAt : alert.firedAt;
                return (
                  <span className="text-gray-500 dark:text-gray-400">
                    {dayjs(at ?? alert.activeAt).fromNow()}
                  </span>
                );
              },
            },
          ]}
        />
      </Card>
    </div>
  );
};
