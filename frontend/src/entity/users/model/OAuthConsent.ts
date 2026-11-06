export interface OAuthConsentInfo {
  clientId: string;
  clientName: string;
  clientUri?: string;
  redirectHost: string;
  scope: string;
  // True when the client proved its identity with an https metadata document.
  verifiedIdentity: boolean;
}

export interface OAuthConnectedApp {
  id: string;
  clientName: string;
  clientId: string;
  lastUsedAt: string;
  expiresAt: string;
}

export type OAuthValidateResult =
  { ok: true; info: OAuthConsentInfo } | { ok: false; error: string; redirectTo?: string };
