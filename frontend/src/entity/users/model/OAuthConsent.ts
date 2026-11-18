export interface OAuthConsentInfo {
  clientId: string;
  clientName: string;
  clientUri?: string;
  redirectHost: string;
  scope: string;
  // True when the client proved its identity with an https metadata document.
  verifiedIdentity: boolean;
  // The client asked for mcp:write; granted only if the user ticks the toggle.
  writeRequested: boolean;
  // Write was requested but this session hasn't passed MFA, so it can't be granted.
  writeNeedsMfa: boolean;
}

export interface OAuthConnectedApp {
  id: string;
  clientName: string;
  clientId: string;
  lastUsedAt: string;
  expiresAt: string;
  scopes?: string[];
}

export type OAuthValidateResult =
  { ok: true; info: OAuthConsentInfo } | { ok: false; error: string; redirectTo?: string };
