import type { SSOProviderType } from './SSOProviderType';

export interface CreateSSOConnectorRequest {
  name: string;
  providerType: SSOProviderType;
  issuerUrl: string;
  clientId: string;
  clientSecret: string;
  scopes: string;
  groupsClaim: string;
  requireVerifiedEmail: boolean;
  trustIdpMfa: boolean;
  buttonLabel: string;
  enabled: boolean;
}
