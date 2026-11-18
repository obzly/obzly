import type { JSX } from 'react';

import type { UsersSummary } from '../../../entity/users';
import { KpiTile } from '../../../shared/ui/v2';

interface Props {
  // undefined while loading, null when it failed: both show "n/a", never 0.
  summary: UsersSummary | null | undefined;
}

const NA = 'n/a';

export const UsersSummaryComponent = ({ summary }: Props): JSX.Element => {
  const s = summary ?? null;
  const notSeen = s ? s.activeAccounts - s.activeLast7Days : 0;
  return (
    <section
      aria-label="Users summary"
      className="grid grid-cols-1 gap-4 sm:grid-cols-2 xl:grid-cols-4"
    >
      <KpiTile
        label="Users"
        value={s ? s.total.toLocaleString() : NA}
        note={
          s
            ? `${s.activeAccounts} active · ${s.invited} invited · ${s.deactivated} deactivated. No seat limit.`
            : 'no data'
        }
      />
      <KpiTile
        label="Active, 7 days"
        value={s ? s.activeLast7Days.toLocaleString() : NA}
        unit={s ? `of ${s.activeAccounts}` : undefined}
        color="var(--v2-blue)"
        note={
          s
            ? notSeen > 0
              ? `${notSeen} active account${notSeen === 1 ? ' has' : 's have'} not been seen this week`
              : 'every active account was seen this week'
            : 'no data'
        }
      />
      <KpiTile
        label="MFA on"
        value={s ? s.mfaEnabled.toLocaleString() : NA}
        unit={s ? `of ${s.activeAccounts} active` : undefined}
        color="var(--v2-purple)"
        note={
          s ? (
            s.adminsWithoutMfa > 0 ? (
              <span className="text-[var(--v2-red)]">
                {s.adminsWithoutMfa} admin{s.adminsWithoutMfa === 1 ? '' : 's'} without MFA
              </span>
            ) : (
              'every admin has MFA'
            )
          ) : (
            'no data'
          )
        }
      />
      <KpiTile
        label="Admins"
        value={s ? s.admins.toLocaleString() : NA}
        color="var(--v2-orange)"
        note={s ? 'manage Obzly settings and every user' : 'no data'}
      />
    </section>
  );
};
