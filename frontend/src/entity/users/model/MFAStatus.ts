export interface MFAStatus {
  enabled: boolean;
  required: boolean;
  // This session's second factor was verified outside Obzly: by the
  // control plane, or by an SSO provider the connector trusts to enforce MFA.
  satisfiedByIdentityProvider?: boolean;
}
