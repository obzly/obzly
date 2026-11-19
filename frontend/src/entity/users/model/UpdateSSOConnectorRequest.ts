import type { SSOMFATrust } from './SSOMFATrust';
import type { SSOProviderType } from './SSOProviderType';

export interface UpdateSSOConnectorRequest {
  name: string;
  providerType: SSOProviderType;
  issuerUrl: string;
  clientId: string;
  // Blank means "keep the existing secret unchanged".
  clientSecret?: string;
  scopes: string;
  groupsClaim: string;
  requireVerifiedEmail: boolean;
  mfaTrust: SSOMFATrust;
  buttonLabel: string;
  enabled: boolean;
}
