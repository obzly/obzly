// Where to go after an SSO sign-in round trip. Only the OAuth consent page
// uses it, and only same-app paths are accepted.
const RETURN_TO_KEY = 'obzly_return_to';
const ALLOWED_PREFIX = '/oauth/authorize';

export const rememberReturnTo = (path: string): void => {
  if (path.startsWith(ALLOWED_PREFIX)) {
    sessionStorage.setItem(RETURN_TO_KEY, path);
  }
};

export const consumeReturnTo = (): string | null => {
  const value = sessionStorage.getItem(RETURN_TO_KEY);
  sessionStorage.removeItem(RETURN_TO_KEY);
  return value && value.startsWith(ALLOWED_PREFIX) ? value : null;
};
