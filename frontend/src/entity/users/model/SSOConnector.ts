import type { SSOMFATrust } from './SSOMFATrust';
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
  // When sign-ins here skip Obzly's own TOTP step; absent from servers before v1.2.2.
  mfaTrust?: SSOMFATrust;
  // Deprecated: mfaTrust !== 'off'.
  trustIdpMfa: boolean;
  buttonLabel: string;
  enabled: boolean;
  createdAt: string;
  updatedAt: string;
}
