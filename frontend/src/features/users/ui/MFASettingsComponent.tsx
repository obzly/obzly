import { LoadingOutlined } from '@ant-design/icons';
import { App, Button, Input, QRCode, Spin } from 'antd';
import { useEffect, useState } from 'react';

import { mfaApi } from '../../../entity/users';
import type { MFASetupResponse } from '../../../entity/users/model/MFASetupResponse';
import type { MFAStatus } from '../../../entity/users/model/MFAStatus';

const PILL = 'inline-flex items-center gap-1 rounded-full px-2 py-0.5 text-xs font-medium';
const PILL_OK = `${PILL} bg-[var(--v2-ok-soft)] text-[var(--v2-accent)]`;
const PILL_BAD = `${PILL} bg-red-500/15 text-[var(--v2-red)]`;
const PILL_NEUTRAL = `${PILL} bg-gray-100 text-gray-600 dark:bg-gray-700 dark:text-gray-300`;
const FIELD_LABEL = 'mb-1 text-xs font-medium text-gray-500 dark:text-gray-400';

/** Admins get no disable button: the backend refuses it for them anyway. */
export function MFASettingsComponent() {
  const { message } = App.useApp();
  const [status, setStatus] = useState<MFAStatus | null>(null);
  const [isLoading, setIsLoading] = useState(true);

  const [pendingSetup, setPendingSetup] = useState<MFASetupResponse | null>(null);
  const [enableCode, setEnableCode] = useState('');
  const [isEnabling, setIsEnabling] = useState(false);

  const [isDisabling, setIsDisabling] = useState(false);
  const [disableCode, setDisableCode] = useState('');
  const [showDisableForm, setShowDisableForm] = useState(false);

  const loadStatus = () => {
    setIsLoading(true);
    mfaApi
      .getStatus()
      .then(setStatus)
      .catch((error: unknown) => {
        message.error(error instanceof Error ? error.message : 'Failed to load MFA status');
      })
      .finally(() => setIsLoading(false));
  };

  useEffect(() => {
    loadStatus();
  }, []);

  const startSetup = async () => {
    try {
      const setup = await mfaApi.setup();
      setPendingSetup(setup);
    } catch (error: unknown) {
      message.error(error instanceof Error ? error.message : 'Failed to start MFA setup');
    }
  };

  const confirmEnable = async () => {
    if (enableCode.length !== 6) {
      message.error('Enter the 6-digit code from your authenticator app');
      return;
    }

    setIsEnabling(true);
    try {
      await mfaApi.enable(enableCode);
      message.success('MFA enabled successfully');
      setPendingSetup(null);
      setEnableCode('');
      loadStatus();
    } catch (error: unknown) {
      message.error(error instanceof Error ? error.message : 'Failed to enable MFA');
    } finally {
      setIsEnabling(false);
    }
  };

  const confirmDisable = async () => {
    if (disableCode.length !== 6) {
      message.error('Enter the 6-digit code from your authenticator app');
      return;
    }

    setIsDisabling(true);
    try {
      await mfaApi.disable(disableCode);
      message.success('MFA disabled successfully');
      setShowDisableForm(false);
      setDisableCode('');
      loadStatus();
    } catch (error: unknown) {
      message.error(error instanceof Error ? error.message : 'Failed to disable MFA');
    } finally {
      setIsDisabling(false);
    }
  };

  if (isLoading) {
    return (
      <div className="flex items-center py-2">
        <Spin indicator={<LoadingOutlined spin />} />
      </div>
    );
  }

  if (!status) {
    return null;
  }

  return (
    <div className="max-w-md">
      <div className="mb-3 flex items-center gap-2">
        <span className="text-sm text-gray-500 dark:text-gray-400">Status:</span>
        {status.enabled ? (
          <span className={PILL_OK}>Enabled</span>
        ) : status.satisfiedByIdentityProvider ? (
          <span className={PILL_OK}>Handled by your identity provider</span>
        ) : (
          <span className={status.required ? PILL_BAD : PILL_NEUTRAL}>
            {status.required ? 'Required, not set up' : 'Not enabled'}
          </span>
        )}
      </div>

      {!status.enabled && status.satisfiedByIdentityProvider && (
        <div className="mb-3 rounded-xl bg-gray-50 px-3.5 py-3 text-[13.5px] text-gray-600 dark:bg-gray-900/40 dark:text-gray-300">
          You signed in through a provider that enforces MFA, so no Obzly authenticator is needed
          for this session. Setting one up is only needed to sign in with a password.
        </div>
      )}

      {status.enabled ? (
        <div>
          {status.required ? (
            <div className="rounded-xl bg-gray-50 px-3.5 py-3 text-[13.5px] text-gray-600 dark:bg-gray-900/40 dark:text-gray-300">
              MFA is required for Admin accounts and can&apos;t be disabled. If you&apos;ve lost
              your device, ask another admin to reset it for you.
            </div>
          ) : !showDisableForm ? (
            <Button danger onClick={() => setShowDisableForm(true)}>
              Disable MFA
            </Button>
          ) : (
            <div className="max-w-[260px] rounded-xl border border-gray-200 p-3 dark:border-gray-700">
              <div className={FIELD_LABEL}>Enter your current 6-digit code to confirm</div>
              <Input
                placeholder="000000"
                value={disableCode}
                onChange={(e) =>
                  setDisableCode(e.currentTarget.value.replace(/\D/g, '').slice(0, 6))
                }
                maxLength={6}
                inputMode="numeric"
                className="!font-mono"
              />
              <div className="mt-2 flex gap-2">
                <Button danger loading={isDisabling} onClick={confirmDisable}>
                  Confirm disable
                </Button>
                <Button
                  type="default"
                  onClick={() => {
                    setShowDisableForm(false);
                    setDisableCode('');
                  }}
                >
                  Cancel
                </Button>
              </div>
            </div>
          )}
        </div>
      ) : pendingSetup ? (
        <div className="flex flex-col items-start gap-3 rounded-xl border border-gray-200 p-4 dark:border-gray-700">
          <div className="text-sm text-gray-600 dark:text-gray-300">
            Scan this QR code with an authenticator app, then enter the code it shows.
          </div>
          <QRCode value={pendingSetup.otpAuthUrl} size={160} bgColor="#ffffff" />
          <div className="text-xs text-gray-500 dark:text-gray-400">
            Or enter this code manually:
            <div className="mt-1 rounded-lg border border-gray-200 bg-gray-50 px-2 py-1 font-mono text-xs break-all dark:border-gray-700 dark:bg-gray-900">
              {pendingSetup.secret}
            </div>
          </div>

          <div className="w-full max-w-[240px]">
            <div className={FIELD_LABEL}>6-digit code</div>
            <Input
              placeholder="000000"
              value={enableCode}
              onChange={(e) => setEnableCode(e.currentTarget.value.replace(/\D/g, '').slice(0, 6))}
              maxLength={6}
              inputMode="numeric"
              onPressEnter={confirmEnable}
              className="!font-mono"
            />
            <div className="mt-2 flex gap-2">
              <Button
                type="primary"
                loading={isEnabling}
                onClick={confirmEnable}
                className="border-accent-600 bg-accent-600 hover:border-accent-700 hover:bg-accent-700"
              >
                Confirm and enable
              </Button>
              <Button type="default" onClick={() => setPendingSetup(null)}>
                Cancel
              </Button>
            </div>
          </div>
        </div>
      ) : (
        <Button
          type="primary"
          onClick={startSetup}
          className="border-accent-600 bg-accent-600 hover:border-accent-700 hover:bg-accent-700"
        >
          {status.required ? 'Set up MFA now' : 'Enable MFA'}
        </Button>
      )}
    </div>
  );
}
