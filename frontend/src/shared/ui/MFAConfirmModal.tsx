import { Alert, Input, Modal } from 'antd';
import { type JSX, useState } from 'react';

interface Props {
  open: boolean;
  title: string;
  description: string;
  /** Rejects with the backend's error message (e.g. "invalid MFA code") on failure. */
  onConfirm: (code: string) => Promise<void>;
  onCancel: () => void;
  confirmText?: string;
}

/** Step-up MFA for a sensitive action; render only when the user has MFA enabled. */
export function MFAConfirmModal({
  open,
  title,
  description,
  onConfirm,
  onCancel,
  confirmText = 'Confirm',
}: Props): JSX.Element {
  const [code, setCode] = useState('');
  const [isSubmitting, setIsSubmitting] = useState(false);
  const [error, setError] = useState('');

  const reset = () => {
    setCode('');
    setError('');
    setIsSubmitting(false);
  };

  const handleCancel = () => {
    reset();
    onCancel();
  };

  const handleConfirm = async () => {
    if (code.length !== 6) {
      setError('Enter the 6-digit code from your authenticator app');
      return;
    }

    setError('');
    setIsSubmitting(true);
    try {
      await onConfirm(code);
      reset();
    } catch (e) {
      setError(e instanceof Error ? e.message : 'Failed to verify code');
      setIsSubmitting(false);
    }
  };

  return (
    <Modal
      title={title}
      open={open}
      onCancel={handleCancel}
      okText={confirmText}
      okButtonProps={{ danger: true, loading: isSubmitting }}
      onOk={handleConfirm}
      destroyOnClose
    >
      <div className="space-y-3">
        <div className="text-sm text-gray-600 dark:text-gray-300">{description}</div>

        {error && <Alert type="error" message={error} showIcon />}

        <Input
          value={code}
          onChange={(e) => setCode(e.target.value.replace(/\D/g, '').slice(0, 6))}
          onPressEnter={handleConfirm}
          placeholder="123456"
          maxLength={6}
          autoFocus
        />
      </div>
    </Modal>
  );
}
