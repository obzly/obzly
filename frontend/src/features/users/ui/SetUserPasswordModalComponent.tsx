import { CopyOutlined, LoadingOutlined, ReloadOutlined } from '@ant-design/icons';
import { App, Button, Input, Modal, Spin } from 'antd';
import { useState } from 'react';

import { userManagementApi } from '../../../entity/users/api/userManagementApi';
import type { UserProfile } from '../../../entity/users/model/UserProfile';
import { copyToClipboard } from '../../../shared/lib';

interface Props {
  user: UserProfile;
  onClose: () => void;
  onPasswordSet: () => void;
}

const generatePassword = (): string => {
  const alphabet = 'ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz23456789';
  const bytes = new Uint32Array(16);
  crypto.getRandomValues(bytes);
  return Array.from(bytes, (b) => alphabet[b % alphabet.length]).join('');
};

/**
 * Admin sets a user's password, e.g. for an invited account when no email is
 * configured; the admin relays the generated password out of band.
 */
export const SetUserPasswordModalComponent = ({ user, onClose, onPasswordSet }: Props) => {
  const { message } = App.useApp();
  const [password, setPassword] = useState(generatePassword());
  const [isSubmitting, setIsSubmitting] = useState(false);

  const handleCopy = async () => {
    if (await copyToClipboard(password)) {
      message.success('Password copied to clipboard');
    } else {
      message.error('Failed to copy password');
    }
  };

  const handleSetPassword = async () => {
    if (password.length < 8) {
      message.error('Password must be at least 8 characters');
      return;
    }

    setIsSubmitting(true);
    try {
      await userManagementApi.setUserPassword(user.id, password);
      message.success(`Password set for ${user.email}`);
      onPasswordSet();
      onClose();
    } catch (error) {
      message.error((error as Error).message || 'Failed to set password');
    } finally {
      setIsSubmitting(false);
    }
  };

  return (
    <Modal
      title={`Set password for ${user.email}`}
      open
      onCancel={onClose}
      footer={[
        <Button key="cancel" onClick={onClose} disabled={isSubmitting}>
          Cancel
        </Button>,
        <Button
          key="set"
          type="primary"
          onClick={handleSetPassword}
          disabled={isSubmitting || password.length < 8}
          className="border-emerald-600 bg-emerald-600 hover:border-emerald-700 hover:bg-emerald-700"
        >
          {isSubmitting ? (
            <Spin indicator={<LoadingOutlined spin />} size="small" />
          ) : (
            'Set password'
          )}
        </Button>,
      ]}
    >
      <p className="mb-3 text-sm text-gray-500 dark:text-gray-300">
        This immediately sets the user&apos;s password - if they haven&apos;t completed their invite
        yet, this also activates their account. There&apos;s no email sender configured, so copy
        this and send it to them yourself.
      </p>
      <div className="mb-2 flex gap-2">
        <Input
          value={password}
          onChange={(e) => setPassword(e.target.value)}
          disabled={isSubmitting}
          autoFocus
        />
        <Button icon={<CopyOutlined />} onClick={handleCopy} disabled={isSubmitting} />
        <Button
          icon={<ReloadOutlined />}
          onClick={() => setPassword(generatePassword())}
          disabled={isSubmitting}
          title="Generate a new password"
        />
      </div>
      {password.length > 0 && password.length < 8 && (
        <p className="text-xs text-red-500">Password must be at least 8 characters.</p>
      )}
    </Modal>
  );
};
