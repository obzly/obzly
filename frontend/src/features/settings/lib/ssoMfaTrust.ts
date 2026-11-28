import type { SSOConnector, SSOMFATrust } from '../../../entity/users';

export interface MFATrustOption {
  value: SSOMFATrust;
  label: string;
  /** Short form for the connector list. */
  shortLabel: string;
  description: string;
}

export const MFA_TRUST_OPTIONS: MFATrustOption[] = [
  {
    value: 'off',
    label: "Always use Obzly's MFA",
    shortLabel: 'Obzly MFA',
    description:
      "Sign-ins through this provider are asked for Obzly's authenticator code (MFA is required for every account).",
  },
  {
    value: 'amr',
    label: 'When the provider reports it',
    shortLabel: 'Provider (if reported)',
    description:
      "Skip Obzly's code only when the ID token's amr claim shows a second factor (mfa, otp, hwk, swk, sms, fido or email_otp). Many providers only send it when configured to.",
  },
  {
    value: 'always',
    label: 'Always trust the provider',
    shortLabel: 'Provider (always)',
    description:
      "Skip Obzly's code for every sign-in through this provider, whatever the ID token says.",
  },
];

export const ALWAYS_TRUST_WARNING =
  "Anyone who can sign in through this provider skips Obzly's MFA - only choose this when the provider requires MFA for every user.";

/** A connector's mode; servers before v1.2.2 only send trustIdpMfa. */
export const connectorMFATrust = (
  connector: Pick<SSOConnector, 'mfaTrust' | 'trustIdpMfa'>,
): SSOMFATrust => {
  if (connector.mfaTrust) {
    return connector.mfaTrust;
  }
  return connector.trustIdpMfa ? 'amr' : 'off';
};

export const mfaTrustOption = (mode: SSOMFATrust): MFATrustOption =>
  MFA_TRUST_OPTIONS.find((option) => option.value === mode) ?? MFA_TRUST_OPTIONS[0];
