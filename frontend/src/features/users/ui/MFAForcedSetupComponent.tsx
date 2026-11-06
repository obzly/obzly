import { Alert, App, Button, Input, QRCode } from 'antd';
import { type JSX, useEffect, useState } from 'react';

import { mfaApi, userApi } from '../../../entity/users';
import type { MFASetupResponse } from '../../../entity/users/model/MFASetupResponse';
import { StringUtils } from '../../../shared/lib';

interface Props {
  /** Called once MFA is successfully enabled, so the caller can drop this gate. */
  onEnabled: () => void;
}

/**
 * Gate for a signed-in Admin whose MFA became required: other routes refuse
 * their token, so this uses the authenticated setup endpoints, not the login ones.
 */
export function MFAForcedSetupComponent({ onEnabled }: Props): JSX.Element {
  const { message } = App.useApp();
  const [setup, setSetup] = useState<MFASetupResponse | null>(null);
  const [code, setCode] = useState('');
  const [isLoading, setLoading] = useState(false);
  const [error, setError] = useState('');

  useEffect(() => {
    mfaApi
      .setup()
      .then(setSetup)
      .catch((e: unknown) => setError(StringUtils.capitalizeFirstLetter((e as Error).message)));
  }, []);

  const submit = async () => {
    if (code.length !== 6) {
      setError('Enter the 6-digit code from your authenticator app');
      return;
    }

    setError('');
    setLoading(true);
    try {
      await mfaApi.enable(code);
      message.success('Two-factor authentication is on');
      onEnabled();
    } catch (e) {
      setError(StringUtils.capitalizeFirstLetter((e as Error).message));
    } finally {
      setLoading(false);
    }
  };

  return (
    <div className="flex h-screen w-screen flex-col items-center justify-center">
      <div className="w-full max-w-[320px]">
        <div className="mb-5 text-center text-2xl font-bold">Set up two-factor authentication</div>

        <div className="mb-4 text-center text-sm text-gray-600 dark:text-gray-300">
          Admin accounts require two-factor authentication. Scan this QR code with an authenticator
          app (Google Authenticator, Authy, 1Password, etc.), then enter the code it shows to
          continue.
        </div>

        {!setup && !error && (
          <div className="py-6 text-center text-sm text-gray-500 dark:text-gray-300">
            Generating your secret...
          </div>
        )}

        {setup && (
          <div className="mb-4 flex flex-col items-center gap-3">
            <QRCode value={setup.otpAuthUrl} size={180} />
            <div className="text-center text-xs text-gray-500 dark:text-gray-300">
              Or enter this code manually:
              <div className="mt-1 rounded bg-gray-50 px-2 py-1 font-mono text-xs break-all dark:bg-gray-900">
                {setup.secret}
              </div>
            </div>

            <div className="w-full">
              <div className="my-1 text-xs font-semibold">6-digit code</div>
              <Input
                placeholder="000000"
                value={code}
                onChange={(e) => {
                  setError('');
                  setCode(e.currentTarget.value.replace(/\D/g, '').slice(0, 6));
                }}
                maxLength={6}
                inputMode="numeric"
                autoFocus
                onPressEnter={submit}
              />

              <div className="mt-3" />

              <Button
                disabled={isLoading}
                loading={isLoading}
                className="w-full"
                onClick={submit}
                type="primary"
              >
                Confirm and continue
              </Button>
            </div>
          </div>
        )}

        {error && <Alert className="mt-3" type="error" message={error} showIcon />}

        <div className="mt-4 text-center text-sm text-gray-600 dark:text-gray-300">
          <button
            type="button"
            onClick={() => {
              userApi.logout();
              window.location.reload();
            }}
            className="cursor-pointer font-medium text-emerald-600 hover:text-emerald-700"
          >
            Sign out
          </button>
        </div>
      </div>
    </div>
  );
}
