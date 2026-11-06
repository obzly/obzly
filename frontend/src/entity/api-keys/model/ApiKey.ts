import type { ApiKeyStatus } from './ApiKeyStatus';

export interface ApiKey {
  id: string;
  name: string;
  projectId: string;
  tokenPrefix: string;
  status: ApiKeyStatus;
  createdAt: Date;
  /** null if the key has never been used. Lags real usage - see backend ApiKeyService.recordLastUsed. */
  lastUsedAt: Date | null;
  token?: string; // Only populated during creation
}
