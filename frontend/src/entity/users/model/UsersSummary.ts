/** The Users page's headline numbers. There is no instance-wide seat limit. */
export interface UsersSummary {
  total: number;
  activeAccounts: number;
  invited: number;
  deactivated: number;
  /** Active accounts seen (sign-in or request) in the last 7 days. */
  activeLast7Days: number;
  /** Matches the activity=inactive filter. */
  inactive: number;
  /** Active accounts with an authenticator, out of activeAccounts. */
  mfaEnabled: number;
  admins: number;
  adminsWithoutMfa: number;
}
