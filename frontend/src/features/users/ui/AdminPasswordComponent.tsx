import { EyeInvisibleOutlined, EyeTwoTone } from '@ant-design/icons';
import { App, Button, Input } from 'antd';
import { type JSX, useState } from 'react';

import { userApi } from '../../../entity/users';
import { MFALoginComponent } from './MFALoginComponent';

interface AdminPasswordComponentProps {
  onPasswordSet?: () => void;
  /** The bootstrap admin's login from the server - "admin", "superadmin" on a control plane, or ADMIN_EMAIL. */
  rootAdminLogin?: string;
}

export function AdminPasswordComponent({
  onPasswordSet,
  rootAdminLogin = 'admin',
}: AdminPasswordComponentProps): JSX.Element {
  const { message } = App.useApp();
  const [bootstrapToken, setBootstrapToken] = useState('');
  const [password, setPassword] = useState('');
  const [passwordVisible, setPasswordVisible] = useState(false);
  const [confirmPassword, setConfirmPassword] = useState('');
  const [confirmPasswordVisible, setConfirmPasswordVisible] = useState(false);

  const [isLoading, setLoading] = useState(false);

  const [passwordError, setPasswordError] = useState(false);
  const [confirmPasswordError, setConfirmPasswordError] = useState(false);

  const [adminPasswordError, setAdminPasswordError] = useState('');
  const [mfaChallenge, setMfaChallenge] = useState<{ mfaToken: string; isSetup: boolean } | null>(
    null,
  );

  const validateFields = (): boolean => {
    if (!password) {
      setPasswordError(true);
      return false;
    }

    if (password.length < 8) {
      setPasswordError(true);
      message.error('Password must be at least 8 characters long');
      return false;
    }
    setPasswordError(false);

    if (!confirmPassword) {
      setConfirmPasswordError(true);
      return false;
    }
    if (password !== confirmPassword) {
      setConfirmPasswordError(true);
      return false;
    }
    setConfirmPasswordError(false);

    return true;
  };

  const onSetPassword = async () => {
    setAdminPasswordError('');

    if (validateFields()) {
      setLoading(true);

      try {
        await userApi.setAdminPassword({
          password,
          bootstrapToken,
        });

        // By the bootstrap admin's real login, which isn't always "admin".
        const signInResponse = await userApi.signIn({
          email: rootAdminLogin,
          password,
        });

        if (signInResponse.mfaToken) {
          // Admins require MFA: continue straight into setup.
          setMfaChallenge({
            mfaToken: signInResponse.mfaToken,
            isSetup: !!signInResponse.mfaSetupRequired,
          });
          return;
        }

        onPasswordSet?.();
      } catch (e) {
        setAdminPasswordError((e as Error).message);
      }
    }

    setLoading(false);
  };

  if (mfaChallenge) {
    return (
      <MFALoginComponent
        mfaToken={mfaChallenge.mfaToken}
        isSetup={mfaChallenge.isSetup}
        onBack={() => {
          setMfaChallenge(null);
          onPasswordSet?.();
        }}
      />
    );
  }

  return (
    <div className="w-full max-w-[300px]">
      <div className="mb-5 text-center text-2xl font-bold">Sign up admin</div>

      <div className="mx-auto mb-4 max-w-[250px] text-center text-sm text-gray-600 dark:text-gray-300">
        Then you will be able to sign in with login &quot;admin&quot; and password you set
      </div>

      <div className="my-1 text-xs font-semibold">Bootstrap token</div>
      <Input
        placeholder="Printed to the server logs on first startup"
        value={bootstrapToken}
        onChange={(e) => setBootstrapToken(e.currentTarget.value.trim())}
      />
      <div className="mx-auto mt-1 mb-3 max-w-[250px] text-center text-xs text-gray-500 dark:text-gray-300">
        Check the server/container logs for a message containing this token - it proves you&apos;re
        the one who deployed this instance.
      </div>

      <div className="my-1 text-xs font-semibold">Password</div>
      <Input.Password
        placeholder="********"
        value={password}
        onChange={(e) => {
          setPasswordError(false);
          setPassword(e.currentTarget.value);
        }}
        status={passwordError ? 'error' : undefined}
        iconRender={(visible) => (visible ? <EyeTwoTone /> : <EyeInvisibleOutlined />)}
        visibilityToggle={{ visible: passwordVisible, onVisibleChange: setPasswordVisible }}
      />

      <div className="my-1 text-xs font-semibold">Confirm password</div>
      <Input.Password
        placeholder="********"
        value={confirmPassword}
        status={confirmPasswordError ? 'error' : undefined}
        onChange={(e) => {
          setConfirmPasswordError(false);
          setConfirmPassword(e.currentTarget.value);
        }}
        iconRender={(visible) => (visible ? <EyeTwoTone /> : <EyeInvisibleOutlined />)}
        visibilityToggle={{
          visible: confirmPasswordVisible,
          onVisibleChange: setConfirmPasswordVisible,
        }}
      />

      <div className="mt-3" />

      <Button
        disabled={isLoading}
        loading={isLoading}
        className="w-full"
        onClick={() => {
          onSetPassword();
        }}
        type="primary"
      >
        Set Password
      </Button>

      {adminPasswordError && (
        <div className="mt-3 flex justify-center text-center text-sm text-red-600">
          {adminPasswordError}
        </div>
      )}
    </div>
  );
}
