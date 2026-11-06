import { IS_CONTROL_PLANE } from '../../../constants';

export type SettingsTab =
  'overview' | 'general' | 'sites' | 'cloud' | 'notifications' | 'identity' | 'updates' | 'audit';

export const TAB_LABEL: Record<SettingsTab, string> = {
  overview: 'Overview',
  general: 'General',
  sites: 'Sites',
  cloud: 'Cloud connections',
  notifications: 'Notifications & alerting',
  identity: 'Identity & access',
  updates: 'Updates',
  audit: 'Audit logs',
};

const TAB_STORAGE_KEY = 'obzly_settings_tab';

/** Makes Obzly settings open on tab next time it renders (e.g. "Open Sites"). */
export const rememberSettingsTab = (tab: SettingsTab) => {
  try {
    localStorage.setItem(TAB_STORAGE_KEY, tab);
  } catch {
    /* opens on Overview */
  }
};

export const readSettingsTab = (): SettingsTab => {
  try {
    const stored = localStorage.getItem(TAB_STORAGE_KEY) as SettingsTab | null;
    // Sites and cloud connections are kept on the control plane only.
    const controlPlaneOnly = stored === 'sites' || stored === 'cloud';
    if (stored && stored in TAB_LABEL && (!controlPlaneOnly || IS_CONTROL_PLANE)) return stored;
  } catch {
    /* default below */
  }
  return 'overview';
};
