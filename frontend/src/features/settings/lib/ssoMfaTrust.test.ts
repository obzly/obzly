import { describe, expect, it } from 'vitest';

import { MFA_TRUST_OPTIONS, connectorMFATrust, mfaTrustOption } from './ssoMfaTrust';

describe('connectorMFATrust', () => {
  it('uses mfaTrust when the server sends it', () => {
    expect(connectorMFATrust({ mfaTrust: 'always', trustIdpMfa: true })).toBe('always');
    expect(connectorMFATrust({ mfaTrust: 'amr', trustIdpMfa: true })).toBe('amr');
    expect(connectorMFATrust({ mfaTrust: 'off', trustIdpMfa: false })).toBe('off');
  });

  it('falls back to the legacy flag from an older server', () => {
    expect(connectorMFATrust({ trustIdpMfa: true })).toBe('amr');
    expect(connectorMFATrust({ trustIdpMfa: false })).toBe('off');
  });
});

describe('mfaTrustOption', () => {
  it('has one option per mode, off first', () => {
    expect(MFA_TRUST_OPTIONS.map((o) => o.value)).toEqual(['off', 'amr', 'always']);
    expect(mfaTrustOption('always').shortLabel).toBe('Provider (always)');
  });

  it('falls back to off for an unknown mode', () => {
    expect(mfaTrustOption('bogus' as never).value).toBe('off');
  });
});
