// The cost use's step in the cloud connection wizard: whose bills, from which source, and what
// each source costs, so the choice is made knowingly.
import { CheckOutlined } from '@ant-design/icons';
import { Alert, Input, InputNumber, Radio, Select } from 'antd';

import type { CloudConnection, CloudProvider, CostSource } from '../../../entity/cloud';
import {
  ADVICE_WEEKLY_REQUESTS,
  COST_REFRESH_HOURS,
  type CostDraft,
  adviceMonthlyUsd,
  costViaOptions,
  estimateCost,
  formatUsd,
} from '../lib/costChoice';

interface Props {
  provider: CloudProvider;
  draft: CostDraft;
  onChange: (draft: CostDraft) => void;
  connections: CloudConnection[];
  selfId?: string;
}

interface SourceCard {
  value: CostSource;
  label: string;
  price: string;
  pros: string[];
  cons: string[];
}

export const CloudCostStep = ({ provider, draft, onChange, connections, selfId }: Props) => {
  const set = (patch: Partial<CostDraft>) => onChange({ ...draft, ...patch });
  const aws = provider === 'aws';
  const estimate = estimateCost(provider, draft.accounts, draft.refreshHours);
  const via = costViaOptions(connections, provider, selfId);
  const storage = aws
    ? 'S3 storage only, usually cents a month; the export is free'
    : 'Storage account only, usually cents a month; the export is free';
  const api = aws
    ? `≈ ${formatUsd(estimate.apiMonthly)} a month (${estimate.apiRequests} Cost Explorer calls at $0.01)`
    : 'Free: the Cost Management API is throttled per tenant instead';

  const cards: SourceCard[] = [
    {
      value: 'auto',
      label: 'Automatic (recommended)',
      price: aws ? `${storage}; API calls only until the first export lands` : storage,
      pros: [
        'Data within minutes from the API, then the export takes over day by day',
        'Single resources and amortized cost once the export has data',
      ],
      cons: ['Needs both the export setup and the API permission'],
    },
    {
      value: 'export',
      label: 'FOCUS export only',
      price: storage,
      pros: [
        'Resource level, effective (amortized) and billed cost',
        aws
          ? 'One export covers every account of the payer'
          : 'One export covers the billing scope',
        'Cheapest at scale; one FOCUS schema for both clouds',
      ],
      cons: [
        'Set up a bucket and the export first',
        'First data can take up to 24 hours',
        'History starts when the export does, unless you backfill it',
      ],
    },
    {
      value: 'api',
      label: 'Billing API only',
      price: api,
      pros: ['Nothing to set up', 'Data within minutes', 'The provider forecast is included'],
      cons: [
        aws ? 'Charged per call' : 'Throttled per tenant',
        'By account, service and region only: no single resources',
        aws ? 'Unblended cost; amortized needs the export' : 'Slower for many subscriptions',
      ],
    },
  ];

  return (
    <div className="flex flex-col gap-4">
      <div className="flex flex-col gap-1">
        <span className="text-xs font-medium text-gray-600 dark:text-gray-300">Whose bills</span>
        <Radio.Group
          className="flex flex-col gap-2"
          value={draft.scope}
          onChange={(event) => set({ scope: event.target.value as CostDraft['scope'] })}
          options={[
            {
              value: 'account',
              label: (
                <span>
                  <span className="font-medium">This {aws ? 'account' : 'subscription'} only</span>
                  <span className="block text-xs text-gray-500 dark:text-gray-400">
                    Its own cost, read with this connection.
                  </span>
                </span>
              ),
            },
            {
              value: 'billing',
              label: (
                <span>
                  <span className="font-medium">
                    {aws
                      ? 'Every account this payer (management account) pays for'
                      : 'The whole billing scope (management group or billing account)'}
                  </span>
                  <span className="block text-xs text-gray-500 dark:text-gray-400">
                    Read once here; member connections then read their cost through this one.
                  </span>
                </span>
              ),
            },
            {
              value: 'via',
              label: (
                <span>
                  <span className="font-medium">Through another connection</span>
                  <span className="block text-xs text-gray-500 dark:text-gray-400">
                    A payer connection already reads it, so nothing is fetched twice.
                  </span>
                </span>
              ),
            },
          ]}
        />
      </div>
      {aws && draft.scope !== 'via' && (
        <span className="-mt-2 text-xs text-gray-500 dark:text-gray-400">
          Recommendations and the commitment planner read Cost Explorer once a week, whatever the
          source: about {ADVICE_WEEKLY_REQUESTS} calls (≈ {formatUsd(adviceMonthlyUsd())} a month).
        </span>
      )}

      {draft.scope === 'via' ? (
        <label className="flex flex-col gap-1">
          <span className="text-xs font-medium text-gray-600 dark:text-gray-300">
            Connection that reads this one&apos;s cost
          </span>
          <Select
            value={draft.viaConnectionId || undefined}
            placeholder="Choose a connection"
            onChange={(value: string) => set({ viaConnectionId: value })}
            options={via.map((connection) => ({ value: connection.id, label: connection.name }))}
            notFoundContent={`No ${aws ? 'AWS' : 'Azure'} connection reads billing for its members yet`}
          />
          <span className="text-[11px] text-gray-500 dark:text-gray-400">
            This connection needs no cost permissions itself.
          </span>
        </label>
      ) : (
        <>
          <div className="flex flex-col gap-1">
            <span className="text-xs font-medium text-gray-600 dark:text-gray-300">
              Data source
            </span>
            <div role="radiogroup" aria-label="Cost data source" className="grid gap-2">
              {cards.map((card) => {
                const selected = draft.source === card.value;
                return (
                  <button
                    key={card.value}
                    type="button"
                    role="radio"
                    aria-checked={selected}
                    onClick={() => set({ source: card.value })}
                    className={`flex cursor-pointer flex-col gap-1.5 rounded-lg border bg-white p-3 text-left dark:bg-gray-900 ${selected ? 'border-emerald-600 ring-2 ring-emerald-600/30' : 'border-gray-200 hover:border-gray-400 dark:border-gray-700'}`}
                  >
                    <span className="flex items-center gap-2 font-medium text-gray-900 dark:text-gray-100">
                      {card.label}
                      {selected && <CheckOutlined className="ml-auto text-emerald-600" />}
                    </span>
                    <span className="text-xs font-medium text-emerald-700 dark:text-emerald-400">
                      {card.price}
                    </span>
                    <span className="grid grid-cols-1 gap-x-4 gap-y-0.5 text-xs sm:grid-cols-2">
                      <span className="flex flex-col gap-0.5 text-gray-600 dark:text-gray-300">
                        {card.pros.map((pro) => (
                          <span key={pro}>+ {pro}</span>
                        ))}
                      </span>
                      <span className="flex flex-col gap-0.5 text-gray-500 dark:text-gray-400">
                        {card.cons.map((con) => (
                          <span key={con}>− {con}</span>
                        ))}
                      </span>
                    </span>
                  </button>
                );
              })}
            </div>
          </div>

          <div className="flex flex-wrap items-end gap-4">
            <label className="flex flex-col gap-1">
              <span className="text-xs font-medium text-gray-600 dark:text-gray-300">
                Refresh every
              </span>
              <Select
                className="w-36"
                value={draft.refreshHours}
                onChange={(value: number) => set({ refreshHours: value })}
                options={COST_REFRESH_HOURS.map((hours) => ({
                  value: hours,
                  label: `${hours} h${hours === 12 ? ' (default)' : ''}`,
                }))}
              />
            </label>
            {aws && draft.source !== 'export' && (
              <label className="flex flex-col gap-1">
                <span className="text-xs font-medium text-gray-600 dark:text-gray-300">
                  Accounts in scope (for the estimate)
                </span>
                <InputNumber
                  className="w-36"
                  min={1}
                  max={5000}
                  value={draft.accounts}
                  onChange={(value) => set({ accounts: value ?? 1 })}
                />
              </label>
            )}
          </div>
          {aws && draft.source !== 'export' && (
            <span className="text-xs text-gray-500 dark:text-gray-400">
              {draft.source === 'api' ? api : `Until the export lands: ${api}`}. The first 90 days
              take about {estimate.backfillRequests} more calls once. Billing data changes a few
              times a day, so refreshing more often mostly adds calls.
            </span>
          )}

          {draft.source !== 'api' && (
            <div className="flex flex-col gap-2 rounded-md border border-gray-200 p-3 dark:border-gray-700">
              <span className="text-sm font-medium text-gray-900 dark:text-gray-100">
                Where the FOCUS export lands
              </span>
              <span className="text-xs text-gray-500 dark:text-gray-400">
                {aws
                  ? 'In the payer account: Billing and Cost Management → Data Exports → Create → FOCUS 1.x with AWS columns, delivered daily (overwrite) to this bucket and prefix.'
                  : 'Cost Management → Exports → Add → FOCUS cost and usage, daily, to this storage account, container and directory.'}
                {draft.source === 'auto' &&
                  ' Leave it empty to start with the API and add it later.'}
              </span>
              {aws ? (
                <div className="grid grid-cols-1 gap-2 sm:grid-cols-3">
                  <Input
                    className="font-mono"
                    placeholder="Bucket, e.g. contoso-billing"
                    value={draft.bucket}
                    onChange={(event) => set({ bucket: event.target.value })}
                  />
                  <Input
                    className="font-mono"
                    placeholder="Region (us-east-1)"
                    value={draft.region}
                    onChange={(event) => set({ region: event.target.value })}
                  />
                  <Input
                    className="font-mono"
                    placeholder="Prefix, e.g. focus"
                    value={draft.prefix}
                    onChange={(event) => set({ prefix: event.target.value })}
                  />
                </div>
              ) : (
                <div className="grid grid-cols-1 gap-2 sm:grid-cols-2">
                  <Input
                    className="font-mono"
                    placeholder="Storage account"
                    value={draft.storageAccount}
                    onChange={(event) => set({ storageAccount: event.target.value })}
                  />
                  <Input
                    className="font-mono"
                    placeholder="Its resource group (for the template)"
                    value={draft.resourceGroup}
                    onChange={(event) => set({ resourceGroup: event.target.value })}
                  />
                  <Input
                    className="font-mono"
                    placeholder="Container, e.g. cost"
                    value={draft.container}
                    onChange={(event) => set({ container: event.target.value })}
                  />
                  <Input
                    className="font-mono"
                    placeholder="Directory, e.g. focus"
                    value={draft.prefix}
                    onChange={(event) => set({ prefix: event.target.value })}
                  />
                </div>
              )}
              <span className="text-[11px] text-gray-500 dark:text-gray-400">
                The template grants read access to {aws ? 'this prefix' : 'this storage account'}{' '}
                only.
              </span>
            </div>
          )}
          {draft.scope === 'billing' && (
            <Alert
              type="info"
              showIcon
              title={
                aws
                  ? 'Run the template in the management account: Cost Explorer and the export there cover every member account.'
                  : 'Assign the roles at the management group or billing scope so every subscription is included.'
              }
            />
          )}
        </>
      )}
    </div>
  );
};
