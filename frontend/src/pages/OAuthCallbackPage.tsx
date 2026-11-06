import { LoadingOutlined } from '@ant-design/icons';
import { Spin } from 'antd';
import { useEffect, useRef, useState } from 'react';
import { useNavigate, useSearchParams } from 'react-router';

import { getOAuthRedirectUri } from '../constants';
import { ssoApi, userApi } from '../entity/users';
import { consumeOAuthState } from '../features/users/lib/oauthState';
import { MFALoginComponent } from '../features/users/ui/MFALoginComponent';
import { consumeReturnTo } from '../shared/lib/returnTo';

export function OAuthCallbackPage() {
  const [searchParams] = useSearchParams();
  const navigate = useNavigate();
  const [error, setError] = useState<string>('');
  const [mfaChallenge, setMfaChallenge] = useState<{ mfaToken: string; isSetup: boolean } | null>(
    null,
  );
  // The stored state is one-shot; StrictMode's second effect run must not
  // consume it again (and fail) or redeem the code twice.
  const handled = useRef(false);

  useEffect(() => {
    if (handled.current) {
      return;
    }
    handled.current = true;

    const handleOAuthCallback = async () => {
      const code = searchParams.get('code');
      const state = searchParams.get('state');

      if (!code) {
        setError('Authorization code not found');
        return;
      }

      if (!state) {
        setError('OAuth state parameter missing');
        return;
      }

      // Only finish a sign-in this browser started (login CSRF).
      if (!consumeOAuthState(state)) {
        sessionStorage.removeItem('oauth_provider');
        setError('This sign-in was not started in this browser. Please sign in again.');
        return;
      }

      const redirectUri = getOAuthRedirectUri();

      // `state` is an opaque signed token, so the provider comes from
      // sessionStorage; cleared at once so it can't leak into a later callback.
      const provider = sessionStorage.getItem('oauth_provider');
      sessionStorage.removeItem('oauth_provider');

      try {
        let response;
        if (provider === 'github') {
          response = await userApi.handleGitHubOAuth({ code, redirectUri, state });
        } else if (provider === 'google') {
          response = await userApi.handleGoogleOAuth({ code, redirectUri, state });
        } else {
          // Otherwise an SSO connector's sign-in.
          response = await ssoApi.handleCallback({ state, code, redirectUri });
        }

        if (response.mfaToken) {
          setMfaChallenge({ mfaToken: response.mfaToken, isSetup: !!response.mfaSetupRequired });
          return;
        }

        navigate(consumeReturnTo() ?? '/', { replace: true });
      } catch (e) {
        setError((e as Error).message || 'OAuth authentication failed');
      }
    };

    handleOAuthCallback();
  }, [searchParams, navigate]);

  if (mfaChallenge) {
    return (
      <div className="flex h-screen w-screen flex-col items-center justify-center">
        <MFALoginComponent
          mfaToken={mfaChallenge.mfaToken}
          isSetup={mfaChallenge.isSetup}
          onBack={() => navigate('/')}
          onSuccess={() => navigate(consumeReturnTo() ?? '/', { replace: true })}
        />
      </div>
    );
  }

  return (
    <div className="flex h-screen w-screen flex-col items-center justify-center">
      {error ? (
        <div>
          <div className="mb-4 text-center text-xl font-semibold text-red-600">
            Authentication Failed
          </div>
          <div className="text-center text-sm text-gray-600 dark:text-gray-300">{error}</div>
          <div className="mt-6 text-center">
            <button
              type="button"
              onClick={() => navigate('/')}
              className="cursor-pointer font-medium text-emerald-600 hover:text-emerald-700"
            >
              Return to sign in
            </button>
          </div>
        </div>
      ) : (
        <div className="flex flex-col items-center">
          <Spin indicator={<LoadingOutlined spin />} size="large" />
          <div className="mt-4 text-gray-600 dark:text-gray-300">Completing authentication...</div>
        </div>
      )}
    </div>
  );
}
