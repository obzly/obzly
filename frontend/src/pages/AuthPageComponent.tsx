import { CopyOutlined, LoadingOutlined } from '@ant-design/icons';
import { App, Button, Spin, Tooltip } from 'antd';
import { useEffect, useState } from 'react';
import { useSearchParams } from 'react-router';

import { CONTROL_PLANE_URL, IS_CONTROL_PLANE, SITE_ID, SITE_NAME } from '../constants';
import { userApi } from '../entity/users';
import {
  AdminPasswordComponent,
  AuthBrandPanelComponent,
  AuthNavbarComponent,
  SignInComponent,
  SignUpComponent,
} from '../features/users';
import { copyToClipboard } from '../shared/lib';

/**
 * On an enrolled Site, signing in here is a break-glass login, so link the
 * control plane too (http(s) only: this is a public page).
 */
function SiteIdentityBanner(): React.JSX.Element | null {
  const { message } = App.useApp();
  if (IS_CONTROL_PLANE || !SITE_ID) return null;

  return (
    // Matches the sign-in form's width.
    <div className="mb-5 w-full max-w-[300px] rounded-md border border-gray-200 px-3 py-2 text-xs text-gray-600 dark:border-gray-700 dark:text-gray-300">
      <div className="flex items-center gap-1">
        <span className="text-gray-500 dark:text-gray-400">Site:</span>
        {SITE_NAME && SITE_NAME !== SITE_ID ? (
          <>
            <b className="truncate">{SITE_NAME}</b>
            <span className="font-mono text-gray-500 dark:text-gray-400">({SITE_ID})</span>
          </>
        ) : (
          <b className="font-mono">{SITE_ID}</b>
        )}
        <Tooltip title="Copy Site ID">
          <Button
            type="text"
            size="small"
            icon={<CopyOutlined />}
            aria-label="Copy Site ID"
            onClick={async () => {
              await copyToClipboard(SITE_ID);
              message.success('Site ID copied');
            }}
          />
        </Tooltip>
      </div>
      {/^https?:\/\//i.test(CONTROL_PLANE_URL) && (
        <div className="mt-0.5">
          This is a local sign-in for this Site. Most users sign in through the{' '}
          <a
            href={CONTROL_PLANE_URL}
            className="text-emerald-600 hover:underline"
            rel="noopener noreferrer"
          >
            control plane
          </a>
          .
        </div>
      )}
    </div>
  );
}

export function AuthPageComponent() {
  const [searchParams] = useSearchParams();
  // Proves the signup comes from whoever holds the invite link; works even
  // with public registration disabled.
  const inviteToken = searchParams.get('invite') ?? undefined;
  const inviteEmail = searchParams.get('email') ?? undefined;

  const [isAdminHasPassword, setIsAdminHasPassword] = useState(false);
  const [isRegistrationEnabled, setIsRegistrationEnabled] = useState(false);
  const [rootAdminLogin, setRootAdminLogin] = useState<string | undefined>(undefined);
  const [authMode, setAuthMode] = useState<'signIn' | 'signUp'>(inviteToken ? 'signUp' : 'signIn');
  const [isLoading, setLoading] = useState(true);

  const checkAdminPasswordStatus = () => {
    setLoading(true);

    userApi
      .isAdminHasPassword()
      .then((response) => {
        setIsAdminHasPassword(response.hasPassword);
        setIsRegistrationEnabled(response.isRegistrationEnabled);
        setRootAdminLogin(response.rootAdminLogin);
        // Registration may have been disabled while this page was open;
        // invites are allowed regardless.
        setAuthMode((prev) =>
          prev === 'signUp' && !response.isRegistrationEnabled && !inviteToken ? 'signIn' : prev,
        );
        setLoading(false);
      })
      .catch((e) => {
        alert('Failed to check admin password status: ' + (e as Error).message);
        // Otherwise a failed request leaves the spinner up forever.
        setLoading(false);
      });
  };

  useEffect(() => {
    checkAdminPasswordStatus();
  }, []);

  if (isLoading) {
    return (
      <div className="flex h-screen w-screen items-center justify-center">
        <Spin indicator={<LoadingOutlined spin />} size="large" />
      </div>
    );
  }

  return (
    <div className="grid min-h-screen lg:grid-cols-[minmax(0,1.1fr)_minmax(0,1fr)]">
      <AuthBrandPanelComponent />

      <div className="flex items-center justify-center p-6">
        <div className="auth-card-in w-full max-w-[380px]">
          {/* Only below lg, where the brand panel is hidden. */}
          <div className="mb-5 lg:hidden">
            <AuthNavbarComponent />
          </div>

          <SiteIdentityBanner />

          {isAdminHasPassword ? (
            authMode === 'signUp' && (isRegistrationEnabled || inviteToken) ? (
              <SignUpComponent
                onSwitchToSignIn={() => setAuthMode('signIn')}
                inviteToken={inviteToken}
                inviteEmail={inviteEmail}
              />
            ) : (
              <SignInComponent
                onSwitchToSignUp={isRegistrationEnabled ? () => setAuthMode('signUp') : undefined}
              />
            )
          ) : (
            <AdminPasswordComponent
              onPasswordSet={checkAdminPasswordStatus}
              rootAdminLogin={rootAdminLogin}
            />
          )}
        </div>
      </div>
    </div>
  );
}
