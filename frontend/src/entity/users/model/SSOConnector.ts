import type { SSOProviderType } from './SSOProviderType';

export interface SSOConnector {
  id: string;
  name: string;
  providerType: SSOProviderType;
  issuerUrl: string;
  clientId: string;
  hasClientSecret: boolean;
  scopes: string;
  // ID-token claim holding the user's groups; empty = group mapping off.
  groupsClaim: string;
  requireVerifiedEmail: boolean;
  // The provider enforces MFA, so sign-ins here skip Obzly's own TOTP step.
  trustIdpMfa: boolean;
  buttonLabel: string;
  enabled: boolean;
  createdAt: string;
  updatedAt: string;
}
