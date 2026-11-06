import { Alert, App, Button, Input, QRCode } from 'antd';
import { type JSX, useEffect, useState } from 'react';

import { mfaApi } from '../../../entity/users';
import type { MFASetupResponse } from '../../../entity/users/model/MFASetupResponse';
import { StringUtils } from '../../../shared/lib';

interface Props {
  /** The short-lived challenge token SignIn/OAuth returned. */
  mfaToken: string;
  /** true: forced first-time setup. false: MFA already enabled, just needs a code. */
  isSetup: boolean;
  onBack: () => void;
  /** The OAuth callback route stays mounted after sign-in, so it must navigate away itself. */
  onSuccess?: () => void;
}

/** Second step of an MFA-gated login; the auth listeners switch the app to signed-in. */
export function MFALoginComponent({ mfaToken, isSetup, onBack, onSuccess }: Props): JSX.Element {
  const { message } = App.useApp();
  const [setup, setSetup] = useState<MFASetupResponse | null>(null);
  const [code, setCode] = useState('');
  const [isLoading, setLoading] = useState(false);
  const [error, setError] = useState('');
  const [isLoadingSetup, setLoadingSetup] = useState(isSetup);

  useEffect(() => {
    if (!isSetup) return;

    mfaApi
      .loginSetup(mfaToken)
      .then(setSetup)
      .catch((e: unknown) => setError(StringUtils.capitalizeFirstLetter((e as Error).message)))
      .finally(() => setLoadingSetup(false));
  }, [isSetup, mfaToken]);

  const submit = async () => {
    if (code.length !== 6) {
      setError('Enter the 6-digit code from your authenticator app');
      return;
    }

    setError('');
    setLoading(true);
    try {
      const response = isSetup
        ? await mfaApi.loginEnable(mfaToken, code)
        : await mfaApi.loginVerify(mfaToken, code);
      if (!response.token) {
        setError('Sign-in did not complete - go back and sign in again');
        return;
      }
      if (isSetup) {
        message.success('Two-factor authentication is on - you are signed in');
      }
      onSuccess?.();
    } catch (e) {
      setError(StringUtils.capitalizeFirstLetter((e as Error).message));
    } finally {
      setLoading(false);
    }
  };

  return (
    <div className="w-full max-w-[320px]">
      <div className="mb-5 text-center text-2xl font-bold">
        {isSetup ? 'Set up two-factor authentication' : 'Enter your code'}
      </div>

      {isSetup && (
        <div className="mb-4 text-center text-sm text-gray-600 dark:text-gray-300">
          Admin accounts require two-factor authentication. Scan this QR code with an authenticator
          app (Google Authenticator, Authy, 1Password, etc.) to continue.
        </div>
      )}

      {isSetup && isLoadingSetup && (
        <div className="py-6 text-center text-sm text-gray-500 dark:text-gray-300">
          Generating your secret...
        </div>
      )}

      {isSetup && setup && (
        <div className="mb-4 flex flex-col items-center gap-3">
          <QRCode value={setup.otpAuthUrl} size={180} />
          <div className="text-center text-xs text-gray-500 dark:text-gray-300">
            Or enter this code manually:
            <div className="mt-1 rounded bg-gray-50 px-2 py-1 font-mono text-xs break-all dark:bg-gray-900">
              {setup.secret}
            </div>
          </div>
        </div>
      )}

      {(!isSetup || setup) && (
        <>
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
            {isSetup ? 'Confirm and sign in' : 'Verify'}
          </Button>
        </>
      )}

      {error && <Alert className="mt-3" type="error" message={error} showIcon />}

      <div className="mt-4 text-center text-sm text-gray-600 dark:text-gray-300">
        <button
          type="button"
          onClick={onBack}
          className="cursor-pointer font-medium text-emerald-600 hover:text-emerald-700"
        >
          Back to sign in
        </button>
      </div>
    </div>
  );
}
