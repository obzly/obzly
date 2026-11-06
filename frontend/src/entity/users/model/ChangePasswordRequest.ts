export interface ChangePasswordRequest {
  currentPassword: string;
  newPassword: string;
  /** Needed when the account has MFA. */
  mfaCode?: string;
}
