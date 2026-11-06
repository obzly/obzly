interface RuntimeConfig {
  IS_CLOUD?: string;
  GITHUB_CLIENT_ID?: string;
  GOOGLE_CLIENT_ID?: string;
  WEBSITE_URL?: string;
  // Multi-site settings; absent means single-site.
  MODE?: 'site' | 'control-plane';
  CONTROL_PLANE_URL?: string;
  SITE_ID?: string;
  SITE_NAME?: string;
}

declare global {
  interface Window {
    __RUNTIME_CONFIG__?: RuntimeConfig;
  }
}

// This deployment's own origin: the base for identity-scoped calls (sign-in,
// SSO, MFA, users, the Site registry). Site data goes through getApiBase().
export function getApplicationServer() {
  const origin = window.location.origin;
  const url = new URL(origin);

  const isDevelopment = import.meta.env.MODE === 'development';

  if (isDevelopment) {
    return `${url.protocol}//${url.hostname}:4005`;
  } else {
    return `${url.protocol}//${url.hostname}:${url.port || (url.protocol === 'https:' ? '443' : '80')}`;
  }
}

// Runtime config only: the same image serves both roles.
export const IS_CONTROL_PLANE = window.__RUNTIME_CONFIG__?.MODE === 'control-plane';

// Only set on a Site, for its login page's "sign in via the control plane" link.
export const CONTROL_PLANE_URL = window.__RUNTIME_CONFIG__?.CONTROL_PLANE_URL || '';
export const SITE_ID = window.__RUNTIME_CONFIG__?.SITE_ID || '';
export const SITE_NAME = window.__RUNTIME_CONFIG__?.SITE_NAME || '';

// Module-level (not React state) so api modules and non-React code can read
// it; src/shared/site's provider is the only writer.
let currentSiteId = '';

export function setCurrentSiteId(siteId: string) {
  currentSiteId = siteId;
}

export function getCurrentSiteId(): string {
  return currentSiteId;
}

// Base URL for Site-scoped calls: on a control plane, its proxy to the selected Site.
export function getApiBase(): string {
  if (IS_CONTROL_PLANE && currentSiteId) {
    return `${getApplicationServer()}/sites/${encodeURIComponent(currentSiteId)}/api/v1`;
  }
  return `${getApplicationServer()}/api/v1`;
}

export const APP_VERSION = (import.meta.env.VITE_APP_VERSION as string) || 'dev';

export const IS_CLOUD =
  window.__RUNTIME_CONFIG__?.IS_CLOUD === 'true' || import.meta.env.VITE_IS_CLOUD === 'true';

export const GITHUB_CLIENT_ID =
  window.__RUNTIME_CONFIG__?.GITHUB_CLIENT_ID || import.meta.env.VITE_GITHUB_CLIENT_ID || '';

export const GOOGLE_CLIENT_ID =
  window.__RUNTIME_CONFIG__?.GOOGLE_CLIENT_ID || import.meta.env.VITE_GOOGLE_CLIENT_ID || '';

// Runtime config first so a self-hosted install can override it without a rebuild.
export const WEBSITE_URL =
  window.__RUNTIME_CONFIG__?.WEBSITE_URL ||
  (import.meta.env.VITE_WEBSITE_URL as string) ||
  'https://obzly.com';

export function getOAuthRedirectUri(): string {
  return `${window.location.origin}/auth/callback`;
}

export function isOAuthEnabled(): boolean {
  return IS_CLOUD && (!!GITHUB_CLIENT_ID || !!GOOGLE_CLIENT_ID);
}
