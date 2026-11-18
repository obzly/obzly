// Settings → General → Appearance: the theme everyone starts from, and whether
// users may pick their own.
import { CopyOutlined } from '@ant-design/icons';
import { App, Button, Segmented, Select, Switch } from 'antd';
import { useState } from 'react';
import type { JSX } from 'react';

import { settingsApi } from '../../../entity/users/api/settingsApi';
import type { UsersSettings } from '../../../entity/users/model/UsersSettings';
import { copyToClipboard } from '../../../shared/lib';
import {
  DARK_THEME_IDS,
  LIGHT_THEME_IDS,
  THEMES,
  type ThemePreference,
  themePolicyFromSettings,
  useTheme,
} from '../../../shared/theme';
import { Card } from '../../../shared/ui/v2';

interface Props {
  initialSettings: UsersSettings;
}

const MODE_LABELS: Record<ThemePreference, string> = {
  light: 'Light',
  dark: 'Dark',
  system: 'System',
};

const themeOptions = (ids: readonly string[]) =>
  ids.map((id) => ({ value: id, label: THEMES[id as keyof typeof THEMES].name }));

export const AppearanceSettingsComponent = ({ initialSettings }: Props): JSX.Element => {
  const { message } = App.useApp();
  const { setPolicy } = useTheme();
  const [policy, setLocalPolicy] = useState(() => themePolicyFromSettings(initialSettings));
  const [isSaving, setIsSaving] = useState(false);

  const kioskLink = `${window.location.origin}/?theme=black`;

  const save = async (patch: Partial<UsersSettings>, done: string) => {
    setIsSaving(true);
    try {
      // Other cards save other fields of the same settings, so merge onto the latest.
      const latest = await settingsApi.getSettings();
      const updated = await settingsApi.updateSettings({ ...latest, ...patch });
      const next = themePolicyFromSettings(updated);
      setLocalPolicy(next);
      setPolicy(next);
      message.success(done);
    } catch (error: unknown) {
      message.error(error instanceof Error ? error.message : 'Failed to update appearance');
    } finally {
      setIsSaving(false);
    }
  };

  return (
    <Card
      title="Appearance"
      subtitle="Defaults for everyone; users can still pick their own in the top bar unless you turn that off."
    >
      <div className="flex flex-col gap-4 text-sm">
        <div className="grid grid-cols-[repeat(auto-fit,minmax(220px,1fr))] gap-4">
          <div className="flex flex-col gap-1.5">
            <span className="text-gray-500 dark:text-gray-300">Default mode for new users</span>
            <Segmented<ThemePreference>
              aria-label="Default mode"
              className="self-start"
              disabled={isSaving}
              value={policy.defaultMode}
              onChange={(mode) =>
                save({ defaultThemeMode: mode }, `Default mode for new users: ${MODE_LABELS[mode]}`)
              }
              options={(['light', 'dark', 'system'] as const).map((mode) => ({
                value: mode,
                label: MODE_LABELS[mode],
              }))}
            />
          </div>
          <label className="flex flex-col gap-1.5">
            <span className="text-gray-500 dark:text-gray-300">Default light theme</span>
            <Select
              aria-label="Default light theme"
              disabled={isSaving}
              value={policy.defaultLight}
              options={themeOptions(LIGHT_THEME_IDS)}
              onChange={(id: string) =>
                save(
                  { defaultLightTheme: id },
                  `Default light theme: ${THEMES[id as keyof typeof THEMES].name}`,
                )
              }
            />
          </label>
          <label className="flex flex-col gap-1.5">
            <span className="text-gray-500 dark:text-gray-300">Default dark theme</span>
            <Select
              aria-label="Default dark theme"
              disabled={isSaving}
              value={policy.defaultDark}
              options={themeOptions(DARK_THEME_IDS)}
              onChange={(id: string) =>
                save(
                  { defaultDarkTheme: id },
                  `Default dark theme: ${THEMES[id as keyof typeof THEMES].name}`,
                )
              }
            />
          </label>
        </div>

        <div className="flex items-start justify-between gap-6 border-t border-gray-200 pt-3 dark:border-gray-700">
          <div className="min-w-0 flex-1">
            <div className="font-medium text-gray-900 dark:text-gray-100">
              Users can choose their own theme
            </div>
            <div className="mt-1 text-gray-500 dark:text-gray-300">
              Off: everyone sees the defaults and the top-bar menu only shows Mode.
            </div>
          </div>
          <Switch
            aria-label="Users can choose their own theme"
            disabled={isSaving}
            checked={policy.userChoiceAllowed}
            onChange={(checked) =>
              save(
                { isUserThemeChoiceAllowed: checked },
                checked
                  ? 'Users can choose their own theme'
                  : 'Everyone now sees the default themes',
              )
            }
          />
        </div>

        <div className="flex flex-col gap-1.5 border-t border-gray-200 pt-3 dark:border-gray-700">
          <div className="font-medium text-gray-900 dark:text-gray-100">
            Wall screens and kiosks
          </div>
          <div className="text-gray-500 dark:text-gray-300">
            Add <span className="font-mono">?theme=</span> to any link to force a theme for that
            screen only (it isn&apos;t saved to anyone&apos;s profile).
          </div>
          <div className="flex flex-wrap items-center gap-2">
            <span className="min-w-0 flex-[1_1_320px] rounded-md border border-gray-200 bg-gray-50 px-2.5 py-1.5 font-mono text-xs break-all text-gray-700 dark:border-gray-700 dark:bg-gray-900 dark:text-gray-200">
              {kioskLink}
            </span>
            <Button
              size="small"
              icon={<CopyOutlined />}
              onClick={async () => {
                await copyToClipboard(kioskLink);
                message.success('Link copied');
              }}
            >
              Copy
            </Button>
          </div>
        </div>
      </div>
    </Card>
  );
};
