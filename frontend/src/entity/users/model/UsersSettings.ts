export interface UsersSettings {
  isAllowExternalRegistrations: boolean;
  isAllowMemberInvitations: boolean;
  isMemberAllowedToCreateProjects: boolean;
  /** Every user, not only admins, sets up MFA at their next sign-in. */
  isMfaRequiredForEveryone?: boolean;
  /** Theme defaults for users who never picked one. */
  defaultThemeMode?: 'light' | 'dark' | 'system';
  defaultLightTheme?: string;
  defaultDarkTheme?: string;
  /** Off: everyone gets the default themes; users still pick the mode. */
  isUserThemeChoiceAllowed?: boolean;
}
