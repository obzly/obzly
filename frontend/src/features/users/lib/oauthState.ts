// Compared on the callback so an attacker's code and state can't complete
// in a victim's browser (login CSRF).
const OAUTH_STATE_KEY = 'obzly_oauth_state';

type StateStorage = Pick<Storage, 'getItem' | 'setItem' | 'removeItem'>;

export const rememberOAuthState = (state: string, storage: StateStorage = sessionStorage): void => {
  storage.setItem(OAUTH_STATE_KEY, state);
};

/** One-shot: true only when returnedState is the one this browser stored. */
export const consumeOAuthState = (
  returnedState: string,
  storage: StateStorage = sessionStorage,
): boolean => {
  const stored = storage.getItem(OAUTH_STATE_KEY);
  storage.removeItem(OAUTH_STATE_KEY);
  return !!stored && stored === returnedState;
};

const LOOPBACK_HOSTS = ['localhost', '127.0.0.1', '[::1]'];

/** https (or http to loopback) only, so an IdP can't send the browser to javascript: etc. */
export const isSafeAuthorizeUrl = (raw: string): boolean => {
  try {
    const url = new URL(raw);
    return (
      url.protocol === 'https:' ||
      (url.protocol === 'http:' && LOOPBACK_HOSTS.includes(url.hostname))
    );
  } catch {
    return false;
  }
};
