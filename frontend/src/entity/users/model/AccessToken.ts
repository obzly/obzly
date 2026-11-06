export interface AccessToken {
  id: string;
  name: string;
  tokenPrefix: string;
  scopes: string[];
  expiresAt: string;
  lastUsedAt?: string | null;
  createdAt: string;
}

export interface CreateAccessTokenResponse {
  // Full token value, shown once.
  token: string;
  accessToken: AccessToken;
}
