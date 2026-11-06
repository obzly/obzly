import type { SSOProviderType } from './SSOProviderType';

export interface PublicSSOConnector {
  id: string;
  name: string;
  providerType: SSOProviderType;
  buttonLabel: string;
  authorizeUrl: string;
}
