// The posture use's step in the cloud connection wizard: native findings, the Obzly baseline, or
// both, with what each costs and covers.
import { CheckOutlined } from '@ant-design/icons';
import { Select, Tag } from 'antd';

import type {
  CloudProvider,
  PostureScanInfo,
  PostureSettings,
  PostureSourceChoice,
} from '../../../entity/cloud';

interface Props {
  provider: CloudProvider;
  settings: PostureSettings;
  onChange: (settings: PostureSettings) => void;
  /** What the last refresh covered, when editing a connection. */
  lastRefresh?: PostureScanInfo;
}

const REFRESH_HOURS = [4, 6, 12, 24];

interface Card {
  value: PostureSourceChoice;
  label: string;
  price: string;
  pros: string[];
  cons: string[];
}

export const CloudPostureStep = ({ provider, settings, onChange, lastRefresh }: Props) => {
  const aws = provider === 'aws';
  const native = aws ? 'Security Hub' : 'Defender for Cloud';
  const nativePrice = aws
    ? 'Security Hub: about $0.001 per security check (less above 100,000 a month), plus the AWS Config rules it uses'
    : 'Foundational CSPM is free; the Defender CSPM plan is billed per server, storage account, database and serverless resource';
  const baselinePrice = 'No service charge: read-only API calls on each refresh';
  const cards: Card[] = [
    {
      value: 'auto',
      label: 'Automatic (recommended)',
      price: `${native} charges where it's on; nothing extra elsewhere`,
      pros: [
        `${native} where it's on, so you see its full set of controls`,
        'The Obzly baseline fills in where it is off or unreadable',
      ],
      cons: ['Findings come from two sources in places; each is labelled'],
    },
    {
      value: 'native',
      label: `${native} only`,
      price: nativePrice,
      pros: [
        'Hundreds of controls, the same results as the provider console',
        'Standards scored the way the provider scores them',
      ],
      cons: [
        `Regions or subscriptions without ${native} aren't covered`,
        "The service's own charges",
      ],
    },
    {
      value: 'baseline',
      label: 'Obzly baseline only',
      price: baselinePrice,
      pros: [
        'Nothing to enable or pay for in the cloud',
        'The same checks across AWS and Azure, with fix steps',
      ],
      cons: ['A smaller set of controls: the most cited CIS, FSBP and MCSB ones'],
    },
  ];

  return (
    <div className="flex flex-col gap-4">
      <div role="radiogroup" aria-label="Posture source" className="grid gap-2">
        {cards.map((card) => {
          const selected = settings.source === card.value;
          return (
            <button
              key={card.value}
              type="button"
              role="radio"
              aria-checked={selected}
              onClick={() => onChange({ ...settings, source: card.value })}
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

      <label className="flex flex-col gap-1">
        <span className="text-xs font-medium text-gray-600 dark:text-gray-300">Refresh every</span>
        <Select
          className="w-36"
          value={settings.refreshHours ?? 12}
          onChange={(value: number) => onChange({ ...settings, refreshHours: value })}
          options={REFRESH_HOURS.map((hours) => ({
            value: hours,
            label: `${hours} h${hours === 12 ? ' (default)' : ''}`,
          }))}
        />
      </label>

      {lastRefresh && (
        <div className="flex flex-col gap-1 rounded-md border border-gray-200 p-3 text-xs dark:border-gray-700">
          <span className="text-sm font-medium text-gray-900 dark:text-gray-100">
            Found on the last refresh
          </span>
          <ScopeList label={native} color="green" scopes={lastRefresh.native} />
          <ScopeList label="Obzly baseline" color="blue" scopes={lastRefresh.baseline} />
          <ScopeList label="Not covered" color="orange" scopes={lastRefresh.notCovered} />
        </div>
      )}
      <span className="text-xs text-gray-500 dark:text-gray-400">
        Each region (AWS) or subscription (Azure) reads from one source, so a finding is never
        counted twice. Test access shows what the probe finds before you save.
      </span>
    </div>
  );
};

const ScopeList = ({
  label,
  color,
  scopes,
}: {
  label: string;
  color: string;
  scopes?: string[];
}) =>
  scopes?.length ? (
    <span className="flex flex-wrap items-center gap-1">
      <span className="w-28 shrink-0 text-gray-500 dark:text-gray-400">{label}</span>
      {scopes.map((scope) => (
        <Tag key={scope} color={color} className="m-0 font-mono text-[11px]">
          {scope}
        </Tag>
      ))}
    </span>
  ) : null;
