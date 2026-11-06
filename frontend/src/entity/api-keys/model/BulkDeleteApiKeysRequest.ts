export interface BulkDeleteApiKeysRequest {
  apiKeyIds: string[];
  /** Required only when the deleting user has MFA enabled. */
  mfaCode?: string;
}
