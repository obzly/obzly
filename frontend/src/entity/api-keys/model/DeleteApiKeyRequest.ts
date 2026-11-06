export interface DeleteApiKeyRequest {
  /** Required only when the deleting user has MFA enabled. */
  mfaCode?: string;
}
