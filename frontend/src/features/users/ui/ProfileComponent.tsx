import { EyeInvisibleOutlined, EyeTwoTone, LoadingOutlined } from '@ant-design/icons';
import { App, Button, Input, Spin } from 'antd';
import { useEffect, useState } from 'react';

import { userApi } from '../../../entity/users/api/userApi';
import type { ChangePasswordRequest } from '../../../entity/users/model/ChangePasswordRequest';
import type { SignInRequest } from '../../../entity/users/model/SignInRequest';
import type { UpdateUserInfoRequest } from '../../../entity/users/model/UpdateUserInfoRequest';
import type { UserProfile } from '../../../entity/users/model/UserProfile';
import { UserRole } from '../../../entity/users/model/UserRole';
import { Card, PageHeader } from '../../../shared/ui/v2';
import { AccessTokensComponent } from './AccessTokensComponent';
import { MFASettingsComponent } from './MFASettingsComponent';

interface Props {
  contentHeight: number;
}

const getRoleDisplayText = (role: UserRole): string => {
  switch (role) {
    case UserRole.ADMIN:
      return 'Admin';
    case UserRole.MEMBER:
      return 'Member';
    default:
      return role;
  }
};

const initialsOf = (source: string): string => {
  const parts = source
    .trim()
    .split(/[\s@._-]+/)
    .filter(Boolean);
  return ((parts[0]?.[0] ?? '?') + (parts[1]?.[0] ?? '')).toUpperCase();
};

export function ProfileComponent({ contentHeight }: Props) {
  const { message } = App.useApp();
  const [user, setUser] = useState<UserProfile | undefined>(undefined);
  const [isChangingPassword, setIsChangingPassword] = useState(false);

  const [editName, setEditName] = useState('');
  const [editEmail, setEditEmail] = useState('');
  const [isUpdatingProfile, setIsUpdatingProfile] = useState(false);
  const [editNameError, setEditNameError] = useState(false);
  const [editEmailError, setEditEmailError] = useState(false);

  // Re-authentication for email and password changes
  const [emailPassword, setEmailPassword] = useState('');
  const [emailMfaCode, setEmailMfaCode] = useState('');

  const [currentPassword, setCurrentPassword] = useState('');
  const [passwordMfaCode, setPasswordMfaCode] = useState('');
  const [newPassword, setNewPassword] = useState('');
  const [confirmPassword, setConfirmPassword] = useState('');
  const [newPasswordVisible, setNewPasswordVisible] = useState(false);
  const [confirmPasswordVisible, setConfirmPasswordVisible] = useState(false);

  const [newPasswordError, setNewPasswordError] = useState(false);
  const [confirmPasswordError, setConfirmPasswordError] = useState(false);

  useEffect(() => {
    loadUserProfile();
  }, []);

  const loadUserProfile = () => {
    userApi
      .getCurrentUser()
      .then((user) => {
        setUser(user);
        setEditName(user.name);
        setEditEmail(user.email);
      })
      .catch((error) => {
        message.error(error.message);
      });
  };

  const validatePasswordFields = (): boolean => {
    let isValid = true;

    if (!currentPassword) {
      message.error('Enter your current password');
      isValid = false;
    }
    if (user?.mfaEnabled && !passwordMfaCode) {
      message.error('Enter the code from your authenticator app');
      isValid = false;
    }
    if (!newPassword) {
      setNewPasswordError(true);
      isValid = false;
    } else if (newPassword.length < 8) {
      setNewPasswordError(true);
      message.error('Password must be at least 8 characters long');
      isValid = false;
    } else {
      setNewPasswordError(false);
    }

    if (!confirmPassword) {
      setConfirmPasswordError(true);
      isValid = false;
    } else if (newPassword !== confirmPassword) {
      setConfirmPasswordError(true);
      message.error('New passwords do not match');
      isValid = false;
    } else {
      setConfirmPasswordError(false);
    }

    return isValid;
  };

  const handlePasswordChange = async () => {
    if (!validatePasswordFields()) {
      return;
    }

    setIsChangingPassword(true);

    try {
      const request: ChangePasswordRequest = {
        currentPassword,
        newPassword,
        ...(user?.mfaEnabled ? { mfaCode: passwordMfaCode } : {}),
      };

      await userApi.changePassword(request);

      setCurrentPassword('');
      setPasswordMfaCode('');
      setNewPassword('');
      setConfirmPassword('');

      // The change ends every session; with MFA, signing in needs a fresh code.
      if (user?.mfaEnabled) {
        message.success('Password changed. Sign in again with your new password.');
        userApi.logout();
        userApi.notifyAuthListeners();
        window.location.reload();
        return;
      }

      if (user?.email) {
        try {
          const signInRequest: SignInRequest = {
            email: user.email,
            password: newPassword,
          };
          await userApi.signIn(signInRequest);
          message.success('Successfully signed in with new password');
        } catch (signInError: unknown) {
          const errorMessage =
            signInError instanceof Error
              ? signInError.message
              : 'Failed to sign in with new password';
          message.error(errorMessage);
          userApi.logout();
          userApi.notifyAuthListeners();
          window.location.reload();
        }
      }
    } catch (error: unknown) {
      const errorMessage = error instanceof Error ? error.message : 'Failed to change password';
      message.error(errorMessage);
    } finally {
      setIsChangingPassword(false);
    }
  };

  const handleProfileUpdate = async () => {
    if (!editName || editName.trim() === '') {
      setEditNameError(true);
      message.error('Name is required');
      return;
    }
    setEditNameError(false);

    // The root admin's email comes from ADMIN_EMAIL.
    if (!user?.isRootAdmin) {
      if (!editEmail || editEmail.trim() === '') {
        setEditEmailError(true);
        message.error('Email is required');
        return;
      }
      setEditEmailError(false);
    }

    setIsUpdatingProfile(true);

    try {
      const request: UpdateUserInfoRequest = {};

      if (editName !== user?.name) {
        request.name = editName;
      }
      if (!user?.isRootAdmin && editEmail !== user?.email) {
        if (!emailPassword || (user?.mfaEnabled && !emailMfaCode)) {
          message.error(
            user?.mfaEnabled
              ? 'Enter your current password and authenticator code to change your email'
              : 'Enter your current password to change your email',
          );
          setIsUpdatingProfile(false);
          return;
        }
        request.email = editEmail;
        request.currentPassword = emailPassword;
        if (user?.mfaEnabled) request.mfaCode = emailMfaCode;
      }

      if (Object.keys(request).length === 0) {
        message.info('No changes to save');
        setIsUpdatingProfile(false);
        return;
      }

      await userApi.updateUserInfo(request);
      message.success('Profile updated successfully');
      setEmailPassword('');
      setEmailMfaCode('');

      loadUserProfile();
    } catch (error: unknown) {
      const errorMessage = error instanceof Error ? error.message : 'Failed to update profile';
      message.error(errorMessage);
    } finally {
      setIsUpdatingProfile(false);
    }
  };

  const handleLogout = () => {
    userApi.logout();
    window.location.reload();
  };

  const label = 'mb-1 block text-xs font-medium text-gray-500 dark:text-gray-400';
  const profileDirty = !!user && (editName !== user.name || editEmail !== user.email);
  const passwordDirty = !!(newPassword || confirmPassword);

  return (
    <div className="flex grow">
      <div className="w-full">
        <div
          className="grow overflow-y-auto rounded-2xl border border-gray-200 bg-white p-5 dark:border-gray-700 dark:bg-gray-800"
          style={{ height: contentHeight }}
        >
          <div className="flex flex-col gap-5">
            <PageHeader
              title="Profile"
              subtitle={user ? `${user.name} · ${user.email}` : undefined}
              actions={
                user && (
                  <Button type="default" onClick={handleLogout} danger>
                    Logout
                  </Button>
                )
              }
            />

            {user ? (
              <>
                <div className="grid grid-cols-1 gap-4 xl:grid-cols-2">
                  <Card title="Profile Information">
                    <div className="flex max-w-md flex-col gap-4">
                      <div className="flex items-center gap-3">
                        <span className="flex h-10 w-10 shrink-0 items-center justify-center rounded-full bg-[var(--v2-ok-soft)] text-sm font-semibold text-[var(--v2-accent)]">
                          {initialsOf(user.name || user.email)}
                        </span>
                        <div className="min-w-0">
                          <div className="truncate font-medium text-gray-900 dark:text-gray-50">
                            {user.name}
                          </div>
                          <div className="truncate text-xs text-gray-500 dark:text-gray-400">
                            {user.email}
                          </div>
                        </div>
                      </div>

                      <div>
                        <div className={label}>User ID</div>
                        <div className="font-mono text-xs break-all text-gray-600 dark:text-gray-300">
                          {user.id}
                        </div>
                      </div>

                      <div>
                        <div className={label}>Name</div>
                        <Input
                          value={editName}
                          onChange={(e) => {
                            setEditNameError(false);
                            setEditName(e.currentTarget.value);
                          }}
                          status={editNameError ? 'error' : undefined}
                          placeholder="Enter your name"
                        />
                      </div>

                      <div>
                        <div className={label}>Email</div>
                        <Input
                          value={editEmail}
                          onChange={(e) => {
                            setEditEmailError(false);
                            setEditEmail(e.currentTarget.value.trim().toLowerCase());
                          }}
                          status={editEmailError ? 'error' : undefined}
                          placeholder="Enter your email"
                          type="email"
                          disabled={!!user.isRootAdmin}
                        />
                        {!!user.isRootAdmin && (
                          <div className="mt-1 text-xs text-gray-500 dark:text-gray-400">
                            The root admin&apos;s login is set by ADMIN_EMAIL, not here
                          </div>
                        )}
                      </div>

                      {!user.isRootAdmin && editEmail !== user.email && (
                        <>
                          <div>
                            <div className={label}>Current password</div>
                            <Input.Password
                              value={emailPassword}
                              onChange={(e) => setEmailPassword(e.currentTarget.value)}
                              placeholder="Confirm it's you to change your email"
                              autoComplete="current-password"
                            />
                          </div>
                          {!!user.mfaEnabled && (
                            <div>
                              <div className={label}>Authenticator code</div>
                              <Input
                                value={emailMfaCode}
                                onChange={(e) => setEmailMfaCode(e.currentTarget.value.trim())}
                                placeholder="6-digit code"
                                inputMode="numeric"
                                autoComplete="one-time-code"
                                maxLength={6}
                                className="!font-mono"
                              />
                            </div>
                          )}
                        </>
                      )}

                      <div>
                        <div className={label}>Role</div>
                        <span className="inline-flex items-center gap-1 rounded-full bg-[var(--v2-ok-soft)] px-2 py-0.5 text-xs font-medium text-[var(--v2-accent)]">
                          {getRoleDisplayText(user.role)}
                        </span>
                      </div>
                    </div>

                    {profileDirty && (
                      <div className="mt-4 flex items-center gap-2 border-t border-gray-200 pt-3 dark:border-gray-700">
                        <span className="flex-1 text-xs text-gray-500 dark:text-gray-400">
                          Unsaved changes
                        </span>
                        <Button
                          type="primary"
                          onClick={handleProfileUpdate}
                          loading={isUpdatingProfile}
                          disabled={isUpdatingProfile}
                          className="border-accent-600 bg-accent-600 hover:border-accent-700 hover:bg-accent-700"
                        >
                          Save changes
                        </Button>
                      </div>
                    )}
                  </Card>

                  <Card title="Change Password">
                    <div className="flex max-w-sm flex-col gap-4">
                      <div>
                        <div className={label}>Current Password</div>
                        <Input.Password
                          placeholder="Enter your current password"
                          value={currentPassword}
                          onChange={(e) => setCurrentPassword(e.currentTarget.value)}
                          autoComplete="current-password"
                        />
                      </div>

                      {!!user.mfaEnabled && (
                        <div>
                          <div className={label}>Authenticator Code</div>
                          <Input
                            placeholder="6-digit code"
                            value={passwordMfaCode}
                            onChange={(e) => setPasswordMfaCode(e.currentTarget.value.trim())}
                            inputMode="numeric"
                            autoComplete="one-time-code"
                            maxLength={6}
                            className="!font-mono"
                          />
                        </div>
                      )}

                      <div>
                        <div className={label}>New Password</div>
                        <Input.Password
                          placeholder="Enter new password"
                          value={newPassword}
                          onChange={(e) => {
                            setNewPasswordError(false);
                            setNewPassword(e.currentTarget.value);
                          }}
                          status={newPasswordError ? 'error' : undefined}
                          iconRender={(visible) =>
                            visible ? <EyeTwoTone /> : <EyeInvisibleOutlined />
                          }
                          visibilityToggle={{
                            visible: newPasswordVisible,
                            onVisibleChange: setNewPasswordVisible,
                          }}
                        />
                      </div>

                      <div>
                        <div className={label}>Confirm New Password</div>
                        <Input.Password
                          placeholder="Confirm new password"
                          value={confirmPassword}
                          onChange={(e) => {
                            setConfirmPasswordError(false);
                            setConfirmPassword(e.currentTarget.value);
                          }}
                          status={confirmPasswordError ? 'error' : undefined}
                          iconRender={(visible) =>
                            visible ? <EyeTwoTone /> : <EyeInvisibleOutlined />
                          }
                          visibilityToggle={{
                            visible: confirmPasswordVisible,
                            onVisibleChange: setConfirmPasswordVisible,
                          }}
                        />
                      </div>
                    </div>

                    {passwordDirty && (
                      <div className="mt-4 flex items-center gap-2 border-t border-gray-200 pt-3 dark:border-gray-700">
                        <span className="flex-1 text-xs text-gray-500 dark:text-gray-400">
                          Unsaved changes
                        </span>
                        <Button
                          type="primary"
                          onClick={handlePasswordChange}
                          loading={isChangingPassword}
                          disabled={isChangingPassword}
                          className="border-accent-600 bg-accent-600 hover:border-accent-700 hover:bg-accent-700"
                        >
                          {isChangingPassword ? 'Changing password...' : 'Change password'}
                        </Button>
                      </div>
                    )}
                  </Card>
                </div>

                <Card title="Two-Factor Authentication">
                  <div className="max-w-xl">
                    <MFASettingsComponent />
                  </div>
                </Card>

                <Card title="AI assistant access (MCP)">
                  <AccessTokensComponent />
                </Card>
              </>
            ) : (
              <div>
                <Spin indicator={<LoadingOutlined spin />} />
              </div>
            )}
          </div>
        </div>
      </div>
    </div>
  );
}
