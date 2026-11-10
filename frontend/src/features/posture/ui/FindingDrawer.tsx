import { CopyOutlined, ExportOutlined, LoadingOutlined } from '@ant-design/icons';
import {
  Alert,
  App,
  Button,
  DatePicker,
  Descriptions,
  Drawer,
  Input,
  Spin,
  Tabs,
  Timeline,
} from 'antd';
import dayjs, { type Dayjs } from 'dayjs';
import { useEffect, useState } from 'react';

import { type FindingDetail, postureApi } from '../../../entity/posture';
import { copyToClipboard } from '../../../shared/lib';
import { regionLabel } from '../lib/format';
import { fillRemediation } from '../lib/merge';
import { SeverityTag, SourceTag } from './parts';

interface Props {
  siteId: string;
  findingId: string;
  onClose: () => void;
  onChanged: () => void;
}

const EVENT_LABEL: Record<string, string> = {
  opened: 'Opened',
  resolved: 'Resolved',
  reopened: 'Reopened',
  suppressed: 'Suppressed',
  unsuppressed: 'Unsuppressed',
};

export const FindingDrawer = ({ siteId, findingId, onClose, onChanged }: Props) => {
  const { message } = App.useApp();
  const [detail, setDetail] = useState<FindingDetail>();
  const [error, setError] = useState<string>();
  const [reason, setReason] = useState('');
  const [until, setUntil] = useState<Dayjs | null>(null);
  const [saving, setSaving] = useState(false);

  useEffect(() => {
    const controller = new AbortController();
    postureApi
      .finding(siteId, findingId, controller.signal)
      .then(setDetail)
      .catch((reason: Error) => {
        if (!controller.signal.aborted) setError(reason.message);
      });
    return () => controller.abort();
  }, [siteId, findingId, saving]);

  const update = async (suppressed: boolean) => {
    setSaving(true);
    try {
      await postureApi.updateFinding(siteId, findingId, {
        suppressed,
        reason: reason.trim(),
        until: suppressed && until ? until.endOf('day').toISOString() : undefined,
      });
      message.success(suppressed ? 'Finding suppressed' : 'Suppression lifted');
      setReason('');
      setUntil(null);
      onChanged();
    } catch (reason) {
      message.error((reason as Error).message);
    } finally {
      setSaving(false);
    }
  };

  const copy = async (text: string) => {
    try {
      await copyToClipboard(text);
      message.success('Copied');
    } catch {
      message.error("Couldn't copy; select the text instead");
    }
  };

  return (
    <Drawer
      open
      onClose={onClose}
      size={Math.min(720, window.innerWidth)}
      title={detail?.title ?? 'Finding'}
    >
      {error && <Alert type="error" showIcon title={error} />}
      {!detail && !error && (
        <div className="flex justify-center py-16">
          <Spin indicator={<LoadingOutlined spin />} />
        </div>
      )}
      {detail && (
        <div className="flex flex-col gap-5">
          <div className="flex flex-wrap items-center gap-2">
            <SeverityTag severity={detail.severity} />
            <SourceTag source={detail.source} />
            <span className="text-sm text-gray-600 dark:text-gray-300">
              {detail.status === 'resolved'
                ? 'Resolved'
                : detail.suppressed
                  ? 'Suppressed'
                  : 'Open'}
            </span>
          </div>

          <Descriptions
            size="small"
            column={1}
            bordered
            items={[
              { key: 'control', label: 'Control', children: detail.control },
              {
                key: 'standards',
                label: 'Standards',
                children: detail.standards.length
                  ? detail.standards
                      .map(
                        (ref) =>
                          `${detail.standardNames[ref.standard] || ref.standard} ${ref.control}`,
                      )
                      .join(' · ')
                  : '—',
              },
              {
                key: 'resource',
                label: 'Resource',
                children: (
                  <span className="flex items-start gap-2">
                    <span className="font-mono text-xs break-all">{detail.resourceKey}</span>
                    <Button
                      size="small"
                      type="text"
                      aria-label="Copy resource ID"
                      icon={<CopyOutlined />}
                      onClick={() => void copy(detail.resourceKey)}
                    />
                  </span>
                ),
              },
              {
                key: 'where',
                label: 'Where',
                children: `${detail.connectionName} · ${detail.account} · ${regionLabel(detail.region)}`,
              },
              {
                key: 'seen',
                label: 'Seen',
                children: `first ${dayjs(detail.firstSeen).format('YYYY-MM-DD HH:mm')}, last ${dayjs(detail.lastSeen).format('YYYY-MM-DD HH:mm')}`,
              },
            ]}
          />

          {Object.keys(detail.evidence).length > 0 && (
            <div>
              <div className="mb-1 text-sm font-semibold text-gray-900 dark:text-gray-100">
                Evidence
              </div>
              <dl className="m-0 grid grid-cols-[minmax(140px,auto)_minmax(0,1fr)] gap-x-3 gap-y-1 text-xs">
                {Object.entries(detail.evidence)
                  .filter(([key]) => !['name', 'resourceGroup', 'location'].includes(key))
                  .map(([key, value]) => (
                    <div key={key} className="contents">
                      <dt className="text-gray-500 dark:text-gray-400">{key}</dt>
                      <dd className="m-0 font-mono break-all text-gray-800 dark:text-gray-200">
                        {value}
                      </dd>
                    </div>
                  ))}
              </dl>
            </div>
          )}

          <div>
            <div className="mb-1 text-sm font-semibold text-gray-900 dark:text-gray-100">
              How to fix
            </div>
            {detail.remediation ? (
              <Tabs
                size="small"
                items={[
                  { key: 'cli', label: 'CLI', text: detail.remediation.cli },
                  { key: 'iac', label: 'IaC', text: detail.remediation.iac },
                  { key: 'console', label: 'Console', text: detail.remediation.console },
                ].map((tab) => ({
                  key: tab.key,
                  label: tab.label,
                  children: (
                    <div className="relative">
                      <pre className="m-0 overflow-auto rounded bg-gray-50 p-3 pr-10 font-mono text-xs whitespace-pre-wrap dark:bg-gray-900">
                        {fillRemediation(tab.text, detail)}
                      </pre>
                      <Button
                        size="small"
                        type="text"
                        aria-label="Copy"
                        className="absolute top-1 right-1"
                        icon={<CopyOutlined />}
                        onClick={() => void copy(fillRemediation(tab.text, detail))}
                      />
                    </div>
                  ),
                }))}
              />
            ) : detail.remediationUrl ? (
              <a href={detail.remediationUrl} target="_blank" rel="noreferrer" className="text-sm">
                The provider&apos;s remediation steps <ExportOutlined />
              </a>
            ) : (
              <span className="text-sm text-gray-500 dark:text-gray-400">
                See the control in the provider&apos;s console.
              </span>
            )}
          </div>

          {detail.status === 'open' && (
            <div className="flex flex-col gap-2 rounded-md border border-gray-200 p-3 dark:border-gray-700">
              {detail.suppressed ? (
                <>
                  <span className="text-sm">
                    Suppressed by {detail.suppressedBy || 'someone'}: {detail.suppressReason}
                    {detail.suppressedUntil
                      ? `, until ${dayjs(detail.suppressedUntil).format('YYYY-MM-DD')}`
                      : ', with no end'}
                  </span>
                  <Input
                    placeholder="Why (optional)"
                    value={reason}
                    maxLength={1000}
                    onChange={(event) => setReason(event.target.value)}
                  />
                  <Button
                    className="self-start"
                    loading={saving}
                    onClick={() => void update(false)}
                  >
                    Lift suppression
                  </Button>
                </>
              ) : (
                <>
                  <span className="text-sm font-medium text-gray-900 dark:text-gray-100">
                    Suppress
                  </span>
                  <span className="text-xs text-gray-500 dark:text-gray-400">
                    A suppressed finding stays tracked but leaves scores and alerts; it counts again
                    when the suppression ends.
                  </span>
                  <Input.TextArea
                    rows={2}
                    maxLength={1000}
                    placeholder="Reason (required), e.g. public website bucket, approved by security"
                    value={reason}
                    onChange={(event) => setReason(event.target.value)}
                  />
                  <span className="flex flex-wrap items-center gap-2">
                    <DatePicker
                      placeholder="Until (empty: no end)"
                      value={until}
                      onChange={setUntil}
                      disabledDate={(day) => day.isBefore(dayjs(), 'day')}
                    />
                    <Button
                      disabled={!reason.trim()}
                      loading={saving}
                      onClick={() => void update(true)}
                    >
                      Suppress
                    </Button>
                  </span>
                </>
              )}
            </div>
          )}

          <div>
            <div className="mb-2 text-sm font-semibold text-gray-900 dark:text-gray-100">
              History
            </div>
            <Timeline
              items={detail.events.map((event) => ({
                content: (
                  <span className="text-xs">
                    <span className="font-medium">{EVENT_LABEL[event.kind] ?? event.kind}</span>{' '}
                    {dayjs(event.at).format('YYYY-MM-DD HH:mm')}
                    {event.by && ` by ${event.by}`}
                    {event.note && (
                      <span className="block text-gray-500 dark:text-gray-400">{event.note}</span>
                    )}
                  </span>
                ),
              }))}
            />
          </div>
        </div>
      )}
    </Drawer>
  );
};
