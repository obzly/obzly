import { Tooltip } from 'antd';

import type { CoverageStatus, PostureSource, Severity } from '../../../entity/posture';
import { useTheme } from '../../../shared/theme';
import { Donut } from '../../../shared/ui/v2';
import { scoreColor } from '../lib/format';
import { SEVERITY_COLOR, SEVERITY_LABEL } from '../lib/merge';

/** Passed of evaluated controls as a ring; nothing evaluated reads n/a, never 0%. */
export const ScoreRing = ({
  name,
  passed,
  evaluated,
  percent,
  size = 72,
}: {
  name: string;
  passed: number;
  evaluated: number;
  percent: number;
  size?: number;
}) => {
  const { status } = useTheme();
  return (
    <Donut
      size={size}
      label={`${name}: ${evaluated ? `${Math.round(percent)}% of controls passed` : 'nothing evaluated yet'}`}
      center={<span className="text-base">{evaluated ? `${Math.round(percent)}%` : 'n/a'}</span>}
      parts={
        evaluated
          ? [
              { key: 'passed', value: passed, color: scoreColor(percent, status) },
              { key: 'failed', value: Math.max(evaluated - passed, 0), color: 'var(--v2-track)' },
            ]
          : []
      }
    />
  );
};

export const SeverityTag = ({ severity }: { severity: Severity }) => (
  <span
    className="inline-flex items-center gap-1.5 rounded-full px-2 py-0.5 text-xs font-semibold whitespace-nowrap"
    style={{ color: SEVERITY_COLOR[severity], background: `${SEVERITY_COLOR[severity]}1f` }}
  >
    <span
      className="inline-block h-2 w-2 rounded-full"
      style={{ background: SEVERITY_COLOR[severity] }}
    />
    {SEVERITY_LABEL[severity]}
  </span>
);

export const SourceTag = ({ source }: { source: PostureSource }) =>
  source === 'native' ? (
    <Tooltip title="From the provider's own service: Security Hub or Defender for Cloud">
      <span className="rounded-full bg-[var(--v2-ok-soft)] px-2 py-0.5 text-xs font-medium whitespace-nowrap text-[var(--v2-accent)]">
        Native
      </span>
    </Tooltip>
  ) : (
    <Tooltip title="From Obzly's own checks, where the native service is off or unreadable">
      <span className="rounded-full bg-sky-500/15 px-2 py-0.5 text-xs font-medium whitespace-nowrap text-[var(--v2-blue)]">
        Obzly baseline
      </span>
    </Tooltip>
  );

const COVERAGE: Record<CoverageStatus, { label: string; className: string }> = {
  native: {
    label: 'Native',
    className: 'bg-emerald-50 text-emerald-700 dark:bg-emerald-950 dark:text-emerald-300',
  },
  fallback: {
    label: 'Fallback: baseline',
    className: 'bg-sky-50 text-sky-700 dark:bg-sky-950 dark:text-sky-300',
  },
  baseline: {
    label: 'Baseline',
    className: 'bg-sky-50 text-sky-700 dark:bg-sky-950 dark:text-sky-300',
  },
  not_covered: {
    label: 'Not covered',
    className: 'bg-amber-50 text-amber-700 dark:bg-amber-950 dark:text-amber-300',
  },
  stale: {
    label: 'Stale',
    className: 'bg-gray-100 text-gray-600 dark:bg-gray-700 dark:text-gray-300',
  },
};

export const CoverageTag = ({ status }: { status: CoverageStatus }) => (
  <span
    className={`rounded-full px-2 py-0.5 text-xs font-medium whitespace-nowrap ${COVERAGE[status].className}`}
  >
    {COVERAGE[status].label}
  </span>
);
