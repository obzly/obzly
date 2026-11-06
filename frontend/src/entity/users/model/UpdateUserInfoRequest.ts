export interface UpdateUserInfoRequest {
  name?: string;
  email?: string;
  /** Needed with an email change. */
  currentPassword?: string;
  /** Needed with an email change when the account has MFA. */
  mfaCode?: string;
}
