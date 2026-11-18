import { AlertOutlined, ApiOutlined, MailOutlined, SlackOutlined } from '@ant-design/icons';
import type { JSX } from 'react';

import type { ChannelKind } from '../../../entity/alerting';

const ICONS: Record<ChannelKind, JSX.Element> = {
  slack: <SlackOutlined />,
  webhook: <ApiOutlined />,
  pagerduty: <AlertOutlined />,
  email: <MailOutlined />,
};

/** Small tile with the channel kind's icon, shown next to its name. */
export const KindIcon = ({ kind }: { kind: ChannelKind }): JSX.Element => (
  <span
    aria-hidden="true"
    className="flex h-7 w-7 shrink-0 items-center justify-center rounded-lg bg-[var(--v2-panel2)] text-gray-500 dark:text-gray-400"
  >
    {ICONS[kind]}
  </span>
);
