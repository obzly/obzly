import { describe, expect, it } from 'vitest';

import { consumeOAuthState, isSafeAuthorizeUrl, rememberOAuthState } from './oauthState';

const memoryStorage = () => {
  const values = new Map<string, string>();
  return {
    getItem: (key: string) => values.get(key) ?? null,
    setItem: (key: string, value: string) => void values.set(key, value),
    removeItem: (key: string) => void values.delete(key),
  };
};

describe('consumeOAuthState', () => {
  it('accepts only the state this browser stored, once', () => {
    const storage = memoryStorage();
    rememberOAuthState('mine', storage);
    expect(consumeOAuthState('mine', storage)).toBe(true);
    expect(consumeOAuthState('mine', storage)).toBe(false);
  });

  it('rejects a state from a sign-in this browser did not start', () => {
    const storage = memoryStorage();
    expect(consumeOAuthState('attacker', storage)).toBe(false);
    rememberOAuthState('mine', storage);
    expect(consumeOAuthState('attacker', storage)).toBe(false);
  });
});

describe('isSafeAuthorizeUrl', () => {
  it('allows https and loopback http', () => {
    expect(isSafeAuthorizeUrl('https://login.example.com/authorize?x=1')).toBe(true);
    expect(isSafeAuthorizeUrl('http://localhost:8080/authorize')).toBe(true);
    expect(isSafeAuthorizeUrl('http://127.0.0.1:5556/auth')).toBe(true);
  });

  it('refuses other schemes and remote http', () => {
    expect(isSafeAuthorizeUrl('javascript:alert(1)')).toBe(false);
    expect(isSafeAuthorizeUrl('data:text/html,hi')).toBe(false);
    expect(isSafeAuthorizeUrl('http://idp.example.com/authorize')).toBe(false);
    expect(isSafeAuthorizeUrl('not a url')).toBe(false);
  });
});
