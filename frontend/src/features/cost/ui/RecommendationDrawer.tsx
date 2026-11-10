// One recommendation: what to change, why (evidence, including the Obzly agent's own CPU and
// memory where it runs on the instance), and its status: plan it, mark it done, dismiss it.
import { CopyOutlined } from '@ant-design/icons';
import {
  App,
  Button,
  DatePicker,
  Descriptions,
  Drawer,
  Input,
  Segmented,
  Tag,
  Tooltip,
} from 'antd';
import dayjs, { type Dayjs } from 'dayjs';
import { useState } from 'react';

import { type RecommendationStatus, costApi } from '../../../entity/cost';
import { copyToClipboard } from '../../../shared/lib';
import {
  BASIS_LABEL,
  KIND_LABEL,
  SOURCE_LABEL,
  STATUS_LABEL,
  type SiteRecommendation,
} from '../lib/advice';
import { formatMoney } from '../lib/format';

interface Props {
  item: SiteRecommendation;
  onClose: () => void;
  onChanged: (item: SiteRecommendation) => void;
}

const AGENT_KEYS = ['agent_cpu_p95', 'agent_memory_p95', 'agent_note'];

const evidenceLabel = (key: string) =>
  ({
    agent_cpu_p95: 'CPU p95, 14 days (Obzly agent)',
    agent_memory_p95: 'Memory p95, 14 days (Obzly agent)',
    agent_note: 'Note',
    cpu_maximum: 'CPU max (CloudWatch)',
    look_back_days: 'Look-back (days)',
  })[key] ?? key.replace(/_/g, ' ');

export const RecommendationDrawer = ({ item, onClose, onChanged }: Props) => {
  const { message } = App.useApp();
  const [mode, setMode] = useState<RecommendationStatus>(
    item.status === 'open' ? 'planned' : item.status,
  );
  const [owner, setOwner] = useState(item.owner ?? '');
  const [due, setDue] = useState<Dayjs | null>(item.dueDate ? dayjs(item.dueDate) : null);
  const [reason, setReason] = useState(item.reason ?? '');
  const [saving, setSaving] = useState(false);

  const save = async (status: RecommendationStatus) => {
    setSaving(true);
    try {
      const updated = await costApi.updateRecommendation(item.siteId, {
        projectId: item.projectId,
        key: item.key,
        status,
        owner: status === 'planned' ? owner.trim() : undefined,
        dueDate: status === 'planned' && due ? due.format('YYYY-MM-DD') : undefined,
        reason: status === 'dismissed' ? reason.trim() : undefined,
      });
      void message.success(
        {
          open: 'Reopened',
          planned: 'Added to the plan',
          done: 'Marked done; realized savings are measured 14 days on',
          dismissed: 'Dismissed',
        }[status],
      );
      onChanged({ ...updated, siteId: item.siteId, siteName: item.siteName });
    } catch (reason) {
      void message.error((reason as Error).message);
    } finally {
      setSaving(false);
    }
  };

  const money = (amount: number) => formatMoney(amount, item.currency || 'USD');
  const evidence = Object.entries(item.evidence ?? {});
  const agent = evidence.filter(([key]) => AGENT_KEYS.includes(key));
  const rest = evidence.filter(([key]) => !AGENT_KEYS.includes(key));
  // A busy memory makes the smaller size risky: shown as a warning.
  const busy = Boolean(item.evidence?.agent_note);
  const tone = busy
    ? 'text-amber-900 dark:text-amber-200'
    : 'text-emerald-900 dark:text-emerald-200';

  return (
    <Drawer open onClose={onClose} size={Math.min(640, window.innerWidth)} title={item.action}>
      <div className="flex flex-col gap-5">
        <Descriptions
          size="small"
          column={1}
          bordered
          items={[
            {
              key: 'resource',
              label: 'Resource',
              children: (
                <span className="flex items-center gap-2">
                  <span className="font-mono text-xs break-all">{item.resourceKey}</span>
                  <Button
                    size="small"
                    type="text"
                    icon={<CopyOutlined />}
                    aria-label="Copy the resource ID"
                    onClick={() => void copyToClipboard(item.resourceKey)}
                  />
                </span>
              ),
            },
            ...(item.resourceName
              ? [{ key: 'name', label: 'Name', children: item.resourceName }]
              : []),
            {
              key: 'where',
              label: 'Where',
              children: [item.connectionName, item.account, item.region]
                .filter(Boolean)
                .join(' · '),
            },
            ...(item.current || item.target
              ? [
                  {
                    key: 'change',
                    label: 'Change',
                    children: `${item.current || '—'} → ${item.target || '—'}`,
                  },
                ]
              : []),
            {
              key: 'savings',
              label: 'Saves',
              children:
                item.monthlySavings === null ? (
                  <Tooltip title="Worked out from the resource's own cost, which needs a cost export with resource-level data">
                    <span className="text-gray-500">n/a until resource-level cost arrives</span>
                  </Tooltip>
                ) : (
                  <span>
                    <b>{money(item.monthlySavings)}</b> a month
                    <span className="block text-xs text-gray-500 dark:text-gray-400">
                      {BASIS_LABEL[item.savingsBasis] ?? ''}
                    </span>
                  </span>
                ),
            },
            {
              key: 'kind',
              label: 'Kind',
              children: `${KIND_LABEL[item.kind] ?? item.kind} · from ${SOURCE_LABEL[item.source] ?? item.source}`,
            },
            { key: 'effort', label: 'Effort', children: item.effort },
            {
              key: 'risk',
              label: 'Performance risk',
              children: (
                <Tag
                  color={item.risk === 'high' ? 'red' : item.risk === 'medium' ? 'orange' : 'green'}
                >
                  {item.risk}
                </Tag>
              ),
            },
          ]}
        />

        {agent.length > 0 && (
          <div
            className={`rounded-md border p-3 text-sm ${busy ? 'border-amber-300 bg-amber-50 dark:border-amber-800 dark:bg-amber-950' : 'border-emerald-200 bg-emerald-50 dark:border-emerald-900 dark:bg-emerald-950'}`}
          >
            <div className={`mb-1 font-medium ${tone}`}>
              Measured by the Obzly agent on the instance
            </div>
            {agent.map(([key, value]) => (
              <div key={key} className={tone}>
                {evidenceLabel(key)}: <b>{key === 'agent_note' ? value : `${value}%`}</b>
              </div>
            ))}
            <div className={`mt-1 text-xs ${tone}`}>
              The cloud&apos;s own metrics have no memory figure; the agent&apos;s do.
            </div>
          </div>
        )}

        {rest.length > 0 && (
          <div>
            <div className="mb-1 text-sm font-medium text-gray-900 dark:text-gray-100">
              Evidence
            </div>
            <dl className="m-0 grid grid-cols-[minmax(0,1fr)_minmax(0,2fr)] gap-x-3 gap-y-1 text-xs">
              {rest.map(([key, value]) => (
                <div key={key} className="contents">
                  <dt className="text-gray-500 dark:text-gray-400">{evidenceLabel(key)}</dt>
                  <dd className="m-0 break-words text-gray-800 dark:text-gray-200">{value}</dd>
                </div>
              ))}
            </dl>
            <p className="mb-0 text-xs text-gray-500 dark:text-gray-400">
              Resource names and tags come from the cloud as they are.
            </p>
          </div>
        )}

        <div className="flex flex-col gap-3">
          <div className="text-sm font-medium text-gray-900 dark:text-gray-100">
            Status: {STATUS_LABEL[item.status]}
            {item.status === 'done' && item.doneBy === 'detected' && (
              <span className="ml-2 text-xs font-normal text-gray-500">
                detected: the provider no longer recommends it
              </span>
            )}
          </div>
          {item.status === 'done' && (
            <div className="text-xs text-gray-600 dark:text-gray-300">
              Expected{' '}
              {item.expectedMonthly !== null && item.expectedMonthly !== undefined
                ? money(item.expectedMonthly)
                : 'n/a'}{' '}
              a month; measured{' '}
              {item.realizedMonthly !== null && item.realizedMonthly !== undefined
                ? money(item.realizedMonthly)
                : item.measuredAt
                  ? 'n/a (not enough cost data around the change)'
                  : `from ${dayjs(item.doneAt).add(16, 'day').format('MMM D')}`}
              .
            </div>
          )}
          <Segmented
            value={mode}
            onChange={(value) => setMode(value as RecommendationStatus)}
            options={[
              { value: 'planned', label: 'Plan' },
              { value: 'done', label: 'Done' },
              { value: 'dismissed', label: 'Dismiss' },
              ...(item.status !== 'open' ? [{ value: 'open', label: 'Reopen' }] : []),
            ]}
          />
          {mode === 'planned' && (
            <div className="flex flex-wrap items-end gap-2">
              <label className="flex flex-col gap-1 text-xs text-gray-600 dark:text-gray-300">
                Owner
                <Input
                  value={owner}
                  onChange={(event) => setOwner(event.target.value)}
                  placeholder="pat@example.com"
                  className="w-56"
                />
              </label>
              <label className="flex flex-col gap-1 text-xs text-gray-600 dark:text-gray-300">
                Due
                <DatePicker value={due} onChange={setDue} />
              </label>
              <Button type="primary" loading={saving} onClick={() => void save('planned')}>
                {item.status === 'planned' ? 'Update plan' : 'Add to plan'}
              </Button>
            </div>
          )}
          {mode === 'done' && (
            <div className="flex flex-col gap-2 text-xs text-gray-600 dark:text-gray-300">
              <span>
                Obzly also marks it done when the provider stops recommending it. Realized savings
                compare the 14 days before with the 14 after, from cost data.
              </span>
              <span>
                <Button type="primary" loading={saving} onClick={() => void save('done')}>
                  Mark done
                </Button>
              </span>
            </div>
          )}
          {mode === 'dismissed' && (
            <div className="flex flex-col gap-2">
              <Input.TextArea
                rows={2}
                value={reason}
                onChange={(event) => setReason(event.target.value)}
                placeholder="Why it doesn't apply (required)"
                maxLength={1000}
              />
              <span>
                <Button
                  danger
                  loading={saving}
                  disabled={!reason.trim()}
                  onClick={() => void save('dismissed')}
                >
                  Dismiss
                </Button>
              </span>
            </div>
          )}
          {mode === 'open' && (
            <span>
              <Button loading={saving} onClick={() => void save('open')}>
                Reopen
              </Button>
            </span>
          )}
          <p className="m-0 text-xs text-gray-500 dark:text-gray-400">
            Obzly only reads: the change is made by your team. Plans and dismissals go into the
            audit log.
          </p>
        </div>
      </div>
    </Drawer>
  );
};
