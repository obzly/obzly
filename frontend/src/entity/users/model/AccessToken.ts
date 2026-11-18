/** The opt-in scope that adds the MCP write tools; every token also has mcp:read. */
export const MCP_WRITE_SCOPE = 'mcp:write';

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
