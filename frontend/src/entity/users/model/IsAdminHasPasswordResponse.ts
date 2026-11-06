export interface IsAdminHasPasswordResponse {
  hasPassword: boolean;
  isRegistrationEnabled: boolean;
  /** The bootstrap admin's login - only while its password isn't set yet. */
  rootAdminLogin?: string;
}
