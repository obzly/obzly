export interface ListUsersRequest {
  limit?: number;
  offset?: number;
  beforeDate?: string;
  query?: string;
  role?: 'ADMIN' | 'MEMBER';
  mfa?: 'on' | 'off';
  /** The account's status. */
  status?: 'ACTIVE' | 'INVITED' | 'INACTIVE';
  /** inactive: not ACTIVE, or no sign-in or request for 30 days; active: the rest. */
  activity?: 'active' | 'inactive';
}
