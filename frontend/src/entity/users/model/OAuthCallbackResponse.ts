// Same MFA gating as SignInResponse.
export interface OAuthCallbackResponse {
  userId?: string;
  email?: string;
  token?: string;
  isNewUser?: boolean;

  mfaRequired?: boolean;
  mfaSetupRequired?: boolean;
  mfaToken?: string;
}
