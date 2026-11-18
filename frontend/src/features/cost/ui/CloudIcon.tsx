// Provider marks and service icons beside the rows that name an account, connection, region,
// standard or resource. Shared with Posture through the feature's index.
import type { ReactNode } from 'react';

import type { CostDimension } from '../../../entity/cost';
import { ProviderLogo } from '../../infrastructure/ui/ProviderLogo';
import {
  type CloudMatch,
  asProvider,
  cloudIcon,
  dimensionIcon,
  regionProvider,
  standardProvider,
} from '../lib/cloudIcons';

/** Draws a match: the service icon, else the provider's mark, else nothing. */
export const CloudMark = ({ match, size = 16 }: { match: CloudMatch; size?: number }) =>
  match.icon ? (
    <img
      src={match.icon}
      alt={match.label ?? ''}
      title={match.label}
      width={size}
      height={size}
      className="shrink-0"
    />
  ) : match.provider ? (
    <ProviderLogo provider={match.provider} size={match.provider === 'aws' ? size - 2 : size} />
  ) : null;

/** The mark of 'aws' or 'azure'; nothing for anything else. */
export const ProviderMark = ({ provider, size }: { provider?: string; size?: number }) => {
  const known = asProvider(provider);
  return known ? <CloudMark match={{ provider: known }} size={size} /> : null;
};

/** A service name, resource type or resource ID's icon, with the provider when it's known. */
export const ServiceIcon = ({
  text,
  provider,
  size,
}: {
  text?: string;
  provider?: string;
  size?: number;
}) => <CloudMark match={cloudIcon(text, provider)} size={size} />;

export const DimensionIcon = ({
  dimension,
  value,
  size,
}: {
  dimension: CostDimension;
  value: string;
  size?: number;
}) => <CloudMark match={dimensionIcon(dimension, value)} size={size} />;

export const RegionMark = ({ region, size }: { region?: string; size?: number }) => (
  <ProviderMark provider={regionProvider(region)} size={size} />
);

export const StandardMark = ({
  standard,
  name,
  size,
}: {
  standard: string;
  name?: string;
  size?: number;
}) => <ProviderMark provider={standardProvider(standard, name)} size={size} />;

/** An icon and its text on one line; the text truncates, the icon doesn't. */
export const IconLabel = ({
  icon,
  children,
  className = '',
}: {
  icon: ReactNode;
  children: ReactNode;
  className?: string;
}) => (
  <span className={`inline-flex max-w-full min-w-0 items-center gap-1.5 ${className}`}>
    {icon}
    <span className="min-w-0 truncate">{children}</span>
  </span>
);
