import { describe, expect, it } from 'vitest';

import type { UpgradeStatus } from '../../../entity/system';
import { type Deployment, compareVersions, upgradeOrder } from './upgradeOrder';

const status = (version: string, blockers: string[] = []): UpgradeStatus =>
  ({ version, verdict: { safeToUpgrade: blockers.length === 0, blockers, warnings: [] } }) as never;

const latest = { status: 'known' as const, version: '1.2.0' };

describe('upgradeOrder', () => {
  it('puts the Sites first and the control plane last, after every Site', () => {
    const deployments: Deployment[] = [
      { key: 'central', name: 'Control plane', role: 'Control plane', status: status('1.1.0') },
      { key: 'site:eu', name: 'Site EU', role: 'Site', status: status('1.1.0') },
      {
        key: 'site:us',
        name: 'Site US',
        role: 'Site',
        status: status('1.1.0', ['3 segments still being rewritten']),
      },
      { key: 'site:ap', name: 'Site APAC', role: 'Site', status: status('1.2.0') },
    ];
    expect(
      upgradeOrder(deployments, latest).map((step) => [step.name, step.state, step.note]),
    ).toEqual([
      ['Site EU', 'ready', 'Ready for v1.2.0'],
      ['Site US', 'waiting', 'Waiting: 3 segments still being rewritten'],
      ['Site APAC', 'current', 'On v1.2.0'],
      ['Control plane', 'after-sites', 'Upgrade after every Site'],
    ]);
  });

  it('lets the control plane go once every Site is current', () => {
    const steps = upgradeOrder(
      [
        { key: 'site:eu', name: 'Site EU', role: 'Site', status: status('1.2.0') },
        { key: 'central', name: 'Control plane', role: 'Control plane', status: status('1.1.0') },
      ],
      latest,
    );
    expect(steps[1]).toMatchObject({ state: 'ready', note: 'Ready for v1.2.0' });
  });

  it('never calls anything current without a known latest release', () => {
    const deployments: Deployment[] = [
      { key: 'site', name: 'This deployment', role: 'Site', status: status('1.2.0') },
    ];
    expect(upgradeOrder(deployments, { status: 'unknown', error: 'GitHub answered 403' })).toEqual(
      [],
    );
    expect(upgradeOrder(deployments, { status: 'disabled' })).toEqual([]);
    expect(upgradeOrder(deployments, undefined)).toEqual([]);
  });

  it("can't tell for unreachable deployments and dev builds", () => {
    const steps = upgradeOrder(
      [
        { key: 'site:eu', name: 'Site EU', role: 'Site', error: 'timeout' },
        { key: 'site:dev', name: 'Site Dev', role: 'Site', status: status('dev') },
      ],
      latest,
    );
    expect(steps.map((step) => [step.state, step.note])).toEqual([
      ['unknown', 'Unreachable: timeout'],
      ['unknown', 'Not a release build'],
    ]);
  });
});

describe('compareVersions', () => {
  it('compares release numbers, not text', () => {
    expect(compareVersions('1.10.0', '1.9.3')).toBeGreaterThan(0);
    expect(compareVersions('v1.2.0', '1.2.0')).toBe(0);
    expect(compareVersions('dev', '1.2.0')).toBeUndefined();
  });
});
