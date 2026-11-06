// With mfaRequired or mfaSetupRequired there is no token; mfaToken is the
// short-lived challenge for mfaApi's login-* calls.
export interface SignInResponse {
  userId?: string;
  email?: string;
  token?: string;

  mfaRequired?: boolean;
  mfaSetupRequired?: boolean;
  mfaToken?: string;
}
