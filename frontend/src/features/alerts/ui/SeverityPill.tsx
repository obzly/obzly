import type { JSX } from 'react';

import type { Severity } from '../../../entity/alerting';
import { severityLabel } from '../lib/messageTemplates';

// Fixed status colours (the same in every theme), like the Slack side bar.
const TONE: Record<Severity, string> = {
  critical: 'bg-red-500/15 text-[var(--v2-red)]',
  high: 'bg-orange-500/15 text-[var(--v2-orange)]',
  medium: 'bg-amber-500/15 text-amber-700 dark:text-amber-400',
  low: 'bg-blue-500/15 text-[var(--v2-blue)]',
};

export const SeverityPill = ({ severity }: { severity?: Severity }): JSX.Element => (
  <span
    className={`inline-flex items-center rounded-full px-2.5 py-0.5 text-xs font-medium whitespace-nowrap ${
      TONE[severity ?? 'medium'] ?? TONE.medium
    }`}
  >
    {severityLabel(severity)}
  </span>
);
