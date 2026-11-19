import type { SSOMFATrust } from './SSOMFATrust';
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
  mfaTrust: SSOMFATrust;
  buttonLabel: string;
  enabled: boolean;
}
