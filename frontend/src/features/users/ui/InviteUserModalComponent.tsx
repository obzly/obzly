// Invites an account to this deployment. With no email delivery, the admin
// relays the one-time link, or uses "Set password" on the new row instead.
import { CopyOutlined } from '@ant-design/icons';
import { App, Button, Input, Modal } from 'antd';
import { useState } from 'react';

import { userApi } from '../../../entity/users';
import { copyToClipboard } from '../../../shared/lib';

interface Props {
  onClose: () => void;
  /** Called after an invite was created, so the caller can refresh its list. */
  onInvited: () => void;
}

const buildInviteLink = (email: string, token: string): string =>
  `${window.location.origin}/?invite=${encodeURIComponent(token)}&email=${encodeURIComponent(email)}`;

export function InviteUserModalComponent({ onClose, onInvited }: Props) {
  const { message } = App.useApp();
  const [email, setEmail] = useState('');
  const [isSubmitting, setIsSubmitting] = useState(false);
  const [invitedEmail, setInvitedEmail] = useState('');
  const [inviteToken, setInviteToken] = useState('');

  const handleInvite = async () => {
    const trimmed = email.trim();
    if (!trimmed) {
      message.error('Enter an email address');
      return;
    }

    setIsSubmitting(true);
    try {
      const response = await userApi.inviteUser({ email: trimmed });
      setInvitedEmail(response.email);
      setInviteToken(response.inviteToken ?? '');
      message.success('Invite created');
      onInvited();
    } catch (e) {
      message.error((e as Error).message);
    }
    setIsSubmitting(false);
  };

  const handleCopy = async () => {
    if (await copyToClipboard(buildInviteLink(invitedEmail, inviteToken))) {
      message.success('Invite link copied');
    } else {
      message.error('Could not copy - select the link and copy it manually');
    }
  };

  return (
    <Modal
      title="Invite user"
      open
      onCancel={onClose}
      footer={
        inviteToken ? (
          <Button type="primary" onClick={onClose}>
            Done
          </Button>
        ) : (
          <>
            <Button onClick={onClose}>Cancel</Button>
            <Button type="primary" loading={isSubmitting} onClick={handleInvite}>
              Create invite
            </Button>
          </>
        )
      }
    >
      {!inviteToken ? (
        <div className="space-y-2">
          <div className="text-sm text-gray-500 dark:text-gray-300">
            Creates an account that the invitee completes with the link shown next - or activate it
            yourself right away with &quot;Set password&quot; on their row.
          </div>
          <Input
            placeholder="name@example.com"
            value={email}
            onChange={(e) => setEmail(e.target.value)}
            onPressEnter={handleInvite}
            autoFocus
          />
        </div>
      ) : (
        <div className="space-y-2">
          <div className="text-sm">
            Invite for <b>{invitedEmail}</b> is ready. Send them this link (shown once - it is not
            stored):
          </div>
          <div className="flex items-center gap-2">
            <Input readOnly value={buildInviteLink(invitedEmail, inviteToken)} />
            <Button icon={<CopyOutlined />} onClick={handleCopy} />
          </div>
          <div className="text-xs text-gray-500 dark:text-gray-300">
            The link expires in 14 days. Alternatively, use &quot;Set password&quot; on the new row
            to activate the account now and hand over the credentials directly.
          </div>
        </div>
      )}
    </Modal>
  );
}
