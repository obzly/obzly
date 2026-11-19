// How far sign-ins through a connector may rely on the provider's MFA
// instead of Obzly's own TOTP.
export type SSOMFATrust = 'off' | 'amr' | 'always';
