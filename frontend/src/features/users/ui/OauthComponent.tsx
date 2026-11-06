import { GithubOutlined, GoogleOutlined, SafetyCertificateOutlined } from '@ant-design/icons';
import { Button, message } from 'antd';
import { useEffect, useState } from 'react';

import {
  GITHUB_CLIENT_ID,
  GOOGLE_CLIENT_ID,
  getOAuthRedirectUri,
  isOAuthEnabled,
} from '../../../constants';
import { ssoApi } from '../../../entity/users/api/ssoApi';
import { userApi } from '../../../entity/users/api/userApi';
import type { PublicSSOConnector } from '../../../entity/users/model/PublicSSOConnector';
import { isSafeAuthorizeUrl, rememberOAuthState } from '../lib/oauthState';

export function OauthComponent() {
  const [ssoConnectors, setSSOConnectors] = useState<PublicSSOConnector[]>([]);

  useEffect(() => {
    const redirectUri = getOAuthRedirectUri();

    ssoApi
      .listPublicConnectors(redirectUri)
      .then(setSSOConnectors)
      .catch(() => {
        // Fall back silently to GitHub/Google, if configured.
        setSSOConnectors([]);
      });
  }, []);

  const showLegacyOAuth = isOAuthEnabled();

  if (!showLegacyOAuth && ssoConnectors.length === 0) {
    return null;
  }

  const redirectUri = getOAuthRedirectUri();

  // `state` is an opaque signed token, so oauth_provider tells the callback
  // page which provider this round trip is for.
  const handleGitHubLogin = async () => {
    if (!GITHUB_CLIENT_ID) {
      message.error('GitHub OAuth is not configured');
      return;
    }

    try {
      const state = await userApi.generateOAuthState('github');
      sessionStorage.setItem('oauth_provider', 'github');
      rememberOAuthState(state);

      const params = new URLSearchParams({
        client_id: GITHUB_CLIENT_ID,
        redirect_uri: redirectUri,
        state,
        scope: 'user:email',
      });

      const githubAuthUrl = `https://github.com/login/oauth/authorize?${params.toString()}`;

      // Throws on a malformed URL.
      new URL(githubAuthUrl);
      window.location.href = githubAuthUrl;
    } catch (error) {
      message.error('Invalid OAuth configuration');
      console.error('GitHub OAuth URL error:', error);
    }
  };

  const handleGoogleLogin = async () => {
    if (!GOOGLE_CLIENT_ID) {
      message.error('Google OAuth is not configured');
      return;
    }

    try {
      const state = await userApi.generateOAuthState('google');
      sessionStorage.setItem('oauth_provider', 'google');
      rememberOAuthState(state);

      const params = new URLSearchParams({
        client_id: GOOGLE_CLIENT_ID,
        redirect_uri: redirectUri,
        response_type: 'code',
        scope: 'openid email profile',
        state,
      });

      const googleAuthUrl = `https://accounts.google.com/o/oauth2/v2/auth?${params.toString()}`;

      // Throws on a malformed URL.
      new URL(googleAuthUrl);
      window.location.href = googleAuthUrl;
    } catch (error) {
      message.error('Invalid OAuth configuration');
      console.error('Google OAuth URL error:', error);
    }
  };

  const handleSSOLogin = (connector: PublicSSOConnector) => {
    try {
      // authorizeUrl is built complete server-side, signed state included.
      if (!isSafeAuthorizeUrl(connector.authorizeUrl)) {
        message.error('Invalid SSO configuration');
        return;
      }
      const state = new URL(connector.authorizeUrl).searchParams.get('state');
      if (!state) {
        message.error('Invalid SSO configuration');
        return;
      }
      sessionStorage.removeItem('oauth_provider');
      rememberOAuthState(state);
      window.location.href = connector.authorizeUrl;
    } catch (error) {
      message.error('Invalid SSO configuration');
      console.error('SSO authorize URL error:', error);
    }
  };

  return (
    <div className="mt-4">
      <div className="space-y-2">
        {showLegacyOAuth && GITHUB_CLIENT_ID && (
          <Button
            icon={<GithubOutlined />}
            onClick={handleGitHubLogin}
            className="w-full"
            size="large"
          >
            Continue with GitHub
          </Button>
        )}

        {showLegacyOAuth && GOOGLE_CLIENT_ID && (
          <Button
            icon={<GoogleOutlined />}
            onClick={handleGoogleLogin}
            className="w-full"
            size="large"
          >
            Continue with Google
          </Button>
        )}

        {ssoConnectors.map((connector) => (
          <Button
            key={connector.id}
            icon={<SafetyCertificateOutlined />}
            onClick={() => handleSSOLogin(connector)}
            className="w-full"
            size="large"
          >
            {connector.buttonLabel}
          </Button>
        ))}
      </div>
    </div>
  );
}
