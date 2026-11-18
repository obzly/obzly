// Settings > Updates: which deployments are behind the latest release, in the order to upgrade
// them. docs/runbooks/upgrade.md: Sites first, the control plane last.
import type { LatestRelease, UpgradeStatus } from '../../../entity/system';

export interface Deployment {
  key: string;
  name: string;
  role: 'Site' | 'Control plane';
  status?: UpgradeStatus;
  error?: string;
}

export type StepState = 'current' | 'ready' | 'waiting' | 'after-sites' | 'unknown';

export interface UpgradeStep {
  key: string;
  name: string;
  role: Deployment['role'];
  /** The running version, as reported. */
  version?: string;
  state: StepState;
  note: string;
}

const SEMVER = /^v?(\d+)\.(\d+)\.(\d+)$/;

/** Negative when a is older than b; undefined when either isn't a release number (a dev build). */
export const compareVersions = (a: string, b: string): number | undefined => {
  const left = SEMVER.exec(a.trim());
  const right = SEMVER.exec(b.trim());
  if (!left || !right) return undefined;
  for (let i = 1; i <= 3; i++) {
    const difference = Number(left[i]) - Number(right[i]);
    if (difference !== 0) return difference;
  }
  return 0;
};

/**
 * Every deployment in upgrade order with what to do about it. Only a known latest release can
 * make a deployment current; without one there are no steps at all.
 */
export const upgradeOrder = (deployments: Deployment[], latest?: LatestRelease): UpgradeStep[] => {
  if (latest?.status !== 'known' || !latest.version) return [];
  const target = latest.version;
  const judged = deployments.map((deployment): UpgradeStep => {
    const base = { key: deployment.key, name: deployment.name, role: deployment.role };
    const version = deployment.status?.version;
    if (!deployment.status || !version) {
      return {
        ...base,
        state: 'unknown',
        note: deployment.error ? `Unreachable: ${deployment.error}` : 'Version not known yet',
      };
    }
    const order = compareVersions(version, target);
    if (order === undefined) {
      return { ...base, version, state: 'unknown', note: 'Not a release build' };
    }
    if (order >= 0) return { ...base, version, state: 'current', note: `On v${target}` };
    const { safeToUpgrade, blockers } = deployment.status.verdict;
    return safeToUpgrade
      ? { ...base, version, state: 'ready', note: `Ready for v${target}` }
      : {
          ...base,
          version,
          state: 'waiting',
          note: `Waiting: ${blockers[0] ?? 'not ready for the next version yet'}`,
        };
  });
  const sites = judged.filter((step) => step.role === 'Site');
  const sitesPending = sites.some((step) => step.state !== 'current');
  const planes = judged
    .filter((step) => step.role === 'Control plane')
    .map((step) =>
      (step.state === 'ready' || step.state === 'waiting') && sitesPending
        ? { ...step, state: 'after-sites' as const, note: 'Upgrade after every Site' }
        : step,
    );
  return [...sites, ...planes];
};
