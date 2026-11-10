// One posture action: the findings it closes, the steps, what it stops coming back, and any cost
// saving on the same resources; plan it or dismiss it.
import { CopyOutlined } from '@ant-design/icons';
import { App, Button, Drawer, Input, Switch, Tag } from 'antd';
import { useState } from 'react';

import { postureApi } from '../../../entity/posture';
import { copyToClipboard } from '../../../shared/lib';
import { formatMoney } from '../../cost/lib/format';
import { KIND_LABEL, STATUS_LABEL, type SiteAction } from '../lib/actions';
import { shortResource } from '../lib/format';
import { SeverityTag } from './parts';

interface Props {
  action: SiteAction;
  onClose: () => void;
  onChanged: (action: SiteAction) => void;
  onFinding: (siteId: string, findingId: string) => void;
}

const looksLikeCommand = (step: string) => /^(aws|az) /.test(step);

export const ActionDrawer = ({ action, onClose, onChanged, onFinding }: Props) => {
  const { message } = App.useApp();
  const [reason, setReason] = useState(action.dismissReason ?? '');
  const [saving, setSaving] = useState(false);

  const update = async (change: { planned?: boolean; dismissed?: boolean }) => {
    setSaving(true);
    try {
      const updated = await postureApi.updateAction(action.siteId, {
        projectId: action.projectId,
        key: action.key,
        ...change,
        reason: change.dismissed ? reason.trim() : undefined,
      });
      void message.success(
        change.planned !== undefined
          ? change.planned
            ? 'Added to the plan'
            : 'Taken out of the plan'
          : change.dismissed
            ? 'Dismissed'
            : 'Restored',
      );
      onChanged({ ...updated, siteId: action.siteId, siteName: action.siteName });
    } catch (reason) {
      void message.error((reason as Error).message);
    } finally {
      setSaving(false);
    }
  };

  return (
    <Drawer open onClose={onClose} size={Math.min(640, window.innerWidth)} title={action.title}>
      <div className="flex flex-col gap-5">
        <div className="flex flex-wrap items-center gap-2 text-sm">
          <Tag className="m-0">{KIND_LABEL[action.kind]}</Tag>
          {action.severity && <SeverityTag severity={action.severity} />}
          <span className="text-gray-500 dark:text-gray-400">Effort: {action.effort}</span>
          <span className="text-gray-500 dark:text-gray-400">
            {[action.connectionName, action.account, action.region].filter(Boolean).join(' · ')}
          </span>
          <Tag
            className="m-0"
            color={
              action.status === 'done'
                ? 'green'
                : action.status === 'accepted_risk'
                  ? 'purple'
                  : 'default'
            }
          >
            {STATUS_LABEL[action.status]}
          </Tag>
        </div>
        {action.detail && (
          <p className="m-0 text-sm text-gray-700 dark:text-gray-300">{action.detail}</p>
        )}

        {action.scopes && action.scopes.length > 0 && (
          <div>
            <div className="mb-1 text-sm font-medium text-gray-900 dark:text-gray-100">Scopes</div>
            <div className="flex flex-wrap gap-1">
              {action.scopes.map((scope) => (
                <Tag key={scope} className="m-0 font-mono text-[11px]">
                  {scope}
                </Tag>
              ))}
            </div>
          </div>
        )}

        {(action.findings ?? []).length > 0 && (
          <div>
            <div className="mb-1 text-sm font-medium text-gray-900 dark:text-gray-100">
              {action.kind === 'guardrail' ? 'Closes' : 'Findings it closes'} (
              {(action.findings ?? []).length})
            </div>
            <div className="flex flex-col gap-1">
              {(action.findings ?? []).map((finding) => (
                <button
                  key={finding.id}
                  type="button"
                  onClick={() => onFinding(action.siteId, finding.id)}
                  className="flex cursor-pointer items-center gap-2 rounded px-2 py-1 text-left text-sm hover:bg-gray-50 dark:hover:bg-gray-700"
                >
                  <SeverityTag severity={finding.severity} />
                  <span className="min-w-0 flex-1 truncate text-gray-800 dark:text-gray-200">
                    {finding.title}
                  </span>
                  <span className="shrink-0 font-mono text-xs text-gray-500">
                    {shortResource(finding.resourceKey)}
                  </span>
                  {finding.status === 'resolved' && (
                    <Tag className="m-0" color="green">
                      Resolved
                    </Tag>
                  )}
                  {finding.suppressed && <Tag className="m-0">Suppressed</Tag>}
                </button>
              ))}
            </div>
          </div>
        )}

        {action.prevents && action.prevents.length > 0 && (
          <div className="text-sm text-gray-700 dark:text-gray-300">
            Stops these coming back on new resources:{' '}
            {action.prevents.map((control) => (
              <Tag key={control} className="m-0 mr-1 font-mono text-[11px]">
                {control}
              </Tag>
            ))}
          </div>
        )}

        {action.steps && action.steps.length > 0 && (
          <div>
            <div className="mb-1 text-sm font-medium text-gray-900 dark:text-gray-100">How</div>
            <div className="flex flex-col gap-2">
              {action.steps.map((step) =>
                looksLikeCommand(step) ? (
                  <div
                    key={step}
                    className="flex items-start gap-2 rounded bg-gray-900 p-2 font-mono text-xs text-gray-100"
                  >
                    <code className="min-w-0 flex-1 break-all whitespace-pre-wrap">{step}</code>
                    <Button
                      size="small"
                      type="text"
                      icon={<CopyOutlined className="text-gray-300" />}
                      aria-label="Copy the command"
                      onClick={() => void copyToClipboard(step)}
                    />
                  </div>
                ) : (
                  <p key={step} className="m-0 text-sm text-gray-700 dark:text-gray-300">
                    {step}
                  </p>
                ),
              )}
            </div>
          </div>
        )}
        {action.kind === 'fix' && (
          <p className="m-0 text-xs text-gray-500 dark:text-gray-400">
            Each finding has its fix steps (CLI, IaC and console) with your resource names filled
            in: click one above.
          </p>
        )}

        {action.costLinks && action.costLinks.length > 0 && (
          <div className="rounded-md border border-emerald-200 bg-emerald-50 p-3 text-sm text-emerald-900 dark:border-emerald-900 dark:bg-emerald-950 dark:text-emerald-200">
            Also on Cost → Recommendations:
            <ul className="m-0 mt-1 pl-4">
              {action.costLinks.map((link) => (
                <li key={link.key}>
                  {link.action}
                  {link.monthlySavings !== null && link.currency
                    ? `: saves ${formatMoney(link.monthlySavings, link.currency)} a month`
                    : ''}
                </li>
              ))}
            </ul>
          </div>
        )}

        <div className="flex flex-col gap-3 border-t border-gray-200 pt-4 dark:border-gray-700">
          <label className="flex items-center gap-2 text-sm text-gray-800 dark:text-gray-200">
            <Switch
              checked={action.planned}
              loading={saving}
              disabled={action.dismissed}
              onChange={(planned) => void update({ planned })}
            />
            In the plan: the standards&apos; cards show the score with it done
          </label>
          {action.dismissed ? (
            <div className="flex flex-col gap-2 text-sm">
              <span className="text-gray-600 dark:text-gray-300">
                Dismissed: {action.dismissReason}
              </span>
              <span>
                <Button loading={saving} onClick={() => void update({ dismissed: false })}>
                  Restore
                </Button>
              </span>
            </div>
          ) : (
            <div className="flex flex-col gap-2">
              <Input.TextArea
                rows={2}
                value={reason}
                onChange={(event) => setReason(event.target.value)}
                placeholder="Why it doesn't apply (required to dismiss)"
                maxLength={1000}
              />
              <span>
                <Button
                  danger
                  loading={saving}
                  disabled={!reason.trim()}
                  onClick={() => void update({ dismissed: true })}
                >
                  Dismiss action
                </Button>
              </span>
            </div>
          )}
          <p className="m-0 text-xs text-gray-500 dark:text-gray-400">
            Done when its findings resolve; if they&apos;re suppressed instead, it&apos;s an
            accepted risk. Obzly changes nothing in the cloud. Plans and dismissals go into the
            audit log.
          </p>
        </div>
      </div>
    </Drawer>
  );
};
