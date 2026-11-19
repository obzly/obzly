// Same MFA gating as SignInResponse.
export interface OAuthCallbackResponse {
  userId?: string;
  email?: string;
  token?: string;
  isNewUser?: boolean;

  mfaRequired?: boolean;
  mfaSetupRequired?: boolean;
  mfaToken?: string;
  // SSO sign-in asked for Obzly's MFA because the provider reported no second factor.
  idpMfaNotReported?: boolean;
}
