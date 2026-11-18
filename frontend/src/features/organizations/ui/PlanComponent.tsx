import { LoadingOutlined } from '@ant-design/icons';
import { App, Button, InputNumber, Spin } from 'antd';
import { useEffect, useState } from 'react';

import { IS_CONTROL_PLANE } from '../../../constants';
import {
  type OrgDetail,
  type OrgPlanLimits,
  organizationsApi,
} from '../../../entity/organizations';
import { Card, Gauge } from '../../../shared/ui/v2';
import { errorMessage } from './lib';

interface Props {
  org: OrgDetail;
  // Global admins set plans; an owner setting their own would be no quota.
  canEdit: boolean;
}

interface Field {
  key: keyof OrgPlanLimits;
  label: string;
  unit: string;
}

const siteFields: Field[] = [
  { key: 'logsPerSecondLimit', label: 'Ingest rate per project', unit: 'logs/s' },
  { key: 'maxLogSizeKb', label: 'Largest log', unit: 'KB' },
  { key: 'maxLogsAmount', label: 'Logs kept per project', unit: 'logs' },
  { key: 'maxLogsSizeMb', label: 'Storage per project', unit: 'MB' },
  { key: 'maxLogsLifeDays', label: 'Retention (logs, traces, metrics)', unit: 'days' },
  { key: 'maxProjects', label: 'Projects per Site', unit: 'projects' },
];

const orgFields: Field[] = [
  ...(IS_CONTROL_PLANE ? [{ key: 'maxSites' as const, label: 'Sites', unit: 'Sites' }] : []),
  { key: 'maxMembers', label: 'Members', unit: 'members' },
];

const emptyLimits: OrgPlanLimits = {
  logsPerSecondLimit: 0,
  maxLogsAmount: 0,
  maxLogsSizeMb: 0,
  maxLogsLifeDays: 0,
  maxLogSizeKb: 0,
  maxProjects: 0,
  maxSites: 0,
  maxMembers: 0,
};

const pickLimits = (plan: OrgPlanLimits): OrgPlanLimits => {
  const limits = { ...emptyLimits };
  for (const key of Object.keys(emptyLimits) as (keyof OrgPlanLimits)[]) {
    limits[key] = plan[key] ?? 0;
  }
  return limits;
};

export const PlanComponent = ({ org, canEdit }: Props) => {
  const { message } = App.useApp();
  const [limits, setLimits] = useState<OrgPlanLimits>();
  const [saving, setSaving] = useState(false);
  const [memberCount, setMemberCount] = useState<number | null>(null);

  const load = async () => {
    try {
      setLimits(pickLimits(await organizationsApi.getPlan(org.id)));
    } catch (error: unknown) {
      message.error(errorMessage(error, 'Failed to load the plan'));
    }
  };

  const save = async () => {
    if (!limits) return;
    setSaving(true);
    try {
      setLimits(pickLimits(await organizationsApi.setPlan(org.id, limits)));
      message.success(`Plan saved for ${org.name}`);
    } catch (error: unknown) {
      message.error(errorMessage(error, 'Failed to save the plan'));
    } finally {
      setSaving(false);
    }
  };

  useEffect(() => {
    load();
    // Only feeds the usage gauge; the Members tab lists them properly.
    organizationsApi
      .listMembers(org.id)
      .then((members) => setMemberCount(members.length))
      .catch(() => setMemberCount(null));
  }, [org.id]);

  if (!limits) {
    return <Spin indicator={<LoadingOutlined spin />} />;
  }

  const renderField = (field: Field) => (
    <div
      key={field.key}
      className="flex flex-wrap items-center justify-between gap-x-3 gap-y-1 border-t border-gray-200 py-2 first:border-t-0 dark:border-gray-700"
    >
      <span className="text-sm text-gray-700 dark:text-gray-200">{field.label}</span>
      {canEdit ? (
        <InputNumber
          min={0}
          precision={0}
          value={limits[field.key] || null}
          placeholder="Unlimited"
          aria-label={field.label}
          // A fixed-width unit keeps every input the same width, so the rows'
          // inputs line up whatever their unit ("KB" vs "projects").
          addonAfter={<span className="inline-block w-16 text-center">{field.unit}</span>}
          style={{ width: 240, maxWidth: '100%' }}
          onChange={(value) => setLimits({ ...limits, [field.key]: value ?? 0 })}
        />
      ) : (
        <span className="text-sm font-medium text-gray-900 dark:text-gray-100">
          {limits[field.key] ? `${limits[field.key].toLocaleString()} ${field.unit}` : 'Unlimited'}
        </span>
      )}
    </div>
  );

  // Only what the organization's own data tells: members and Sites against their limits.
  const usage: { key: string; label: string; used: number | null; limit: number }[] = [
    { key: 'members', label: 'Members', used: memberCount, limit: limits.maxMembers },
    ...(IS_CONTROL_PLANE
      ? [{ key: 'sites', label: 'Sites', used: org.sites.length, limit: limits.maxSites }]
      : []),
  ];

  const gaugeColor = (ratio: number) =>
    ratio >= 1 ? 'var(--v2-red)' : ratio >= 0.9 ? 'var(--v2-orange)' : 'var(--v2-accent)';

  return (
    <div className="grid grid-cols-1 items-start gap-4 xl:grid-cols-[minmax(0,3fr)_minmax(0,2fr)]">
      <Card
        title="Plan limits"
        subtitle={
          <>
            {IS_CONTROL_PLANE
              ? "Each of the organization's Sites applies these to its projects within a few minutes, " +
                "alongside a project's own limits - the stricter wins, even with a project's rate limiting off."
              : org.slug === 'default'
                ? "This install applies the Default organization's plan to its projects, alongside a " +
                  "project's own limits - the stricter wins, even with a project's rate limiting off."
                : "On this install only the Default organization's plan applies to projects; this one " +
                  'limits members.'}{' '}
            Empty is unlimited.
          </>
        }
        bodyClassName="flex flex-col gap-4"
      >
        <div>
          <h3 className="m-0 mb-1 text-xs font-medium tracking-wide text-gray-500 uppercase dark:text-gray-400">
            {IS_CONTROL_PLANE ? 'On each Site' : 'Projects'}
          </h3>
          {siteFields.map(renderField)}
        </div>
        <div>
          <h3 className="m-0 mb-1 text-xs font-medium tracking-wide text-gray-500 uppercase dark:text-gray-400">
            Organization
          </h3>
          {orgFields.map(renderField)}
        </div>
        {canEdit && (
          <div className="flex items-center justify-end gap-2 border-t border-gray-200 pt-3 dark:border-gray-700">
            <Button type="primary" loading={saving} onClick={save}>
              Save plan
            </Button>
          </div>
        )}
      </Card>
      <Card title="Usage against the plan" subtitle={`What ${org.name} uses today`}>
        <div className="flex flex-wrap justify-around gap-4">
          {usage.map((u) => {
            const ratio = u.used !== null && u.limit > 0 ? u.used / u.limit : null;
            return (
              <div key={u.key} className="flex flex-col items-center gap-1">
                <Gauge
                  size={120}
                  ratio={ratio === null ? null : Math.min(ratio, 1)}
                  color={ratio === null ? undefined : gaugeColor(ratio)}
                  value={u.used === null ? 'n/a' : u.used.toLocaleString()}
                  caption={u.limit > 0 ? `of ${u.limit.toLocaleString()}` : 'Unlimited'}
                  label={`${u.label}: ${u.used === null ? 'no data' : u.used} of ${u.limit > 0 ? u.limit : 'unlimited'}`}
                />
                <strong className="text-sm font-semibold text-gray-900 dark:text-gray-100">
                  {u.label}
                </strong>
              </div>
            );
          })}
        </div>
      </Card>
    </div>
  );
};
