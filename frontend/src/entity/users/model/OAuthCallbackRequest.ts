export interface OAuthCallbackRequest {
  code: string;
  redirectUri: string;
  // The signed CSRF token from userApi.generateOAuthState.
  state: string;
}
