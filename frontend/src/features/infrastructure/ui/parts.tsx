import {
  ApartmentOutlined,
  CloudServerOutlined,
  ContainerOutlined,
  DesktopOutlined,
  ShareAltOutlined,
} from '@ant-design/icons';
import { Tooltip } from 'antd';
import type { ReactNode } from 'react';

import type { AssetHealth, AssetState, SiteAsset } from '../../../entity/inventory';
import { HEALTH_COLOR, HEALTH_LABEL, STATE_LABEL, iconFor, kindLabel } from '../lib/assets';

export const HealthDot = ({ health, size = 10 }: { health: AssetHealth; size?: number }) => (
  <span
    className="inline-block shrink-0 rounded-full"
    style={{ width: size, height: size, background: HEALTH_COLOR[health] }}
    title={HEALTH_LABEL[health]}
  />
);

export const HealthBadge = ({ health }: { health: AssetHealth }) => (
  <span
    className="inline-flex items-center gap-1.5 rounded-full px-2 py-0.5 text-xs font-semibold whitespace-nowrap"
    style={{ color: HEALTH_COLOR[health], background: `${HEALTH_COLOR[health]}1f` }}
  >
    <HealthDot health={health} size={8} />
    {HEALTH_LABEL[health]}
  </span>
);

const GENERIC: Partial<Record<SiteAsset['kind'], ReactNode>> = {
  host: <DesktopOutlined />,
  container: <ContainerOutlined />,
  docker_container: <ContainerOutlined />,
  service: <ShareAltOutlined />,
  workload: <ApartmentOutlined />,
};

export const KindIcon = ({
  asset,
  size = 18,
}: {
  asset: Pick<SiteAsset, 'kind' | 'subtype' | 'attributes'>;
  size?: number;
}) => {
  const src = iconFor(asset);
  if (src) {
    return <img src={src} width={size} height={size} alt="" aria-hidden className="shrink-0" />;
  }
  return (
    <span
      className="inline-flex shrink-0 items-center justify-center text-gray-500 dark:text-gray-400"
      style={{ width: size, height: size, fontSize: size - 3 }}
      aria-hidden
    >
      {GENERIC[asset.kind] ?? <CloudServerOutlined />}
    </span>
  );
};

export const KindLabel = ({ asset }: { asset: SiteAsset }) => (
  <span className="inline-flex items-center gap-1.5 text-xs whitespace-nowrap text-gray-500 dark:text-gray-400">
    <KindIcon asset={asset} />
    {kindLabel(asset)}
  </span>
);

const STATE_CLASS: Record<AssetState, string> = {
  new: 'border-emerald-500 text-emerald-600 dark:text-emerald-400',
  active: 'border-gray-300 text-gray-500 dark:border-gray-600 dark:text-gray-400',
  not_reporting: 'border-red-400 text-red-600 dark:text-red-400',
  terminated: 'border-blue-400 text-blue-600 dark:text-blue-400',
};

export const LifecycleTag = ({ state }: { state: AssetState }) => (
  <span
    className={`rounded-full border px-2 py-px text-[11px] font-semibold whitespace-nowrap ${STATE_CLASS[state]}`}
  >
    {STATE_LABEL[state]}
  </span>
);

/** A percentage with a small bar, amber/red past the thresholds. */
export const PercentBar = ({
  value,
  warnAt = 90,
  criticalAt = 97,
  title,
}: {
  value?: number;
  warnAt?: number;
  criticalAt?: number;
  title?: string;
}) => {
  if (value === undefined) return <span className="text-gray-400 dark:text-gray-500">—</span>;
  const color =
    value >= criticalAt
      ? HEALTH_COLOR.critical
      : value >= warnAt
        ? HEALTH_COLOR.warning
        : '#059669';
  return (
    <Tooltip title={title} placement="top">
      <span className="inline-grid grid-cols-[40px_44px] items-center gap-1.5">
        <span className="text-right tabular-nums">{Math.round(value)}%</span>
        <span className="h-[5px] overflow-hidden rounded bg-gray-200 dark:bg-gray-700">
          <span
            className="block h-full"
            style={{ width: `${Math.min(100, value)}%`, background: color }}
          />
        </span>
      </span>
    </Tooltip>
  );
};
