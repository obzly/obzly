export interface UsersSettings {
  isAllowExternalRegistrations: boolean;
  isAllowMemberInvitations: boolean;
  isMemberAllowedToCreateProjects: boolean;
  /** Every user, not only admins, sets up MFA at their next sign-in. */
  isMfaRequiredForEveryone?: boolean;
}
