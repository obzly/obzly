import { LoadingOutlined } from '@ant-design/icons';
import { App, Button, InputNumber, Spin } from 'antd';
import { useEffect, useState } from 'react';

import { IS_CONTROL_PLANE } from '../../../constants';
import {
  type OrgPlanLimits,
  type Organization,
  organizationsApi,
} from '../../../entity/organizations';
import { errorMessage } from './lib';

interface Props {
  org: Organization;
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
  }, [org.id]);

  if (!limits) {
    return <Spin indicator={<LoadingOutlined spin />} />;
  }

  const renderField = (field: Field) => (
    <div key={field.key} className="flex items-center justify-between gap-3 py-1">
      <span className="text-sm text-gray-700 dark:text-gray-200">{field.label}</span>
      {canEdit ? (
        <InputNumber
          min={0}
          precision={0}
          value={limits[field.key] || null}
          placeholder="Unlimited"
          // A fixed-width unit keeps every input the same width, so the rows'
          // inputs line up whatever their unit ("KB" vs "projects").
          addonAfter={<span className="inline-block w-16 text-center">{field.unit}</span>}
          style={{ width: 240 }}
          onChange={(value) => setLimits({ ...limits, [field.key]: value ?? 0 })}
        />
      ) : (
        <span className="text-sm text-gray-900 dark:text-gray-100">
          {limits[field.key] ? `${limits[field.key].toLocaleString()} ${field.unit}` : 'Unlimited'}
        </span>
      )}
    </div>
  );

  return (
    <div className="max-w-xl">
      <div className="mb-3 text-sm text-gray-500 dark:text-gray-300">
        {IS_CONTROL_PLANE
          ? "Each of the organization's Sites applies these to its projects within a few minutes, " +
            "alongside a project's own limits - the stricter wins, even with a project's rate limiting off."
          : org.slug === 'default'
            ? "This install applies the Default organization's plan to its projects, alongside a " +
              "project's own limits - the stricter wins, even with a project's rate limiting off."
            : "On this install only the Default organization's plan applies to projects; this one " +
              'limits members.'}{' '}
        Empty is unlimited.
      </div>
      <h3 className="mt-4 mb-1 text-sm font-semibold text-gray-900 dark:text-gray-100">
        {IS_CONTROL_PLANE ? 'On each Site' : 'Projects'}
      </h3>
      {siteFields.map(renderField)}
      <h3 className="mt-4 mb-1 text-sm font-semibold text-gray-900 dark:text-gray-100">
        Organization
      </h3>
      {orgFields.map(renderField)}
      {canEdit && (
        <Button type="primary" className="mt-4" loading={saving} onClick={save}>
          Save plan
        </Button>
      )}
    </div>
  );
};
