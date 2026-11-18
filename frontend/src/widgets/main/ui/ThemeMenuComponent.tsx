import { DesktopOutlined, MoonOutlined, SunOutlined } from '@ant-design/icons';
import {
  Alert,
  Button,
  ConfigProvider,
  Popover,
  Segmented,
  Switch,
  Tooltip,
  theme as antdTheme,
} from 'antd';
import { useState } from 'react';
import type { JSX, ReactNode } from 'react';

import {
  DARK_THEME_IDS,
  LIGHT_THEME_IDS,
  THEMES,
  type ThemeId,
  type ThemePreference,
  useTheme,
} from '../../../shared/theme';

const COLOR_BLIND_SWATCHES = ['#0072B2', '#E69F00', '#009E73', '#CC79A7', '#D55E00'];

const ThemePreview = ({ id }: { id: ThemeId }): JSX.Element => {
  const p = THEMES[id].preview;
  const [s0, s1] = THEMES[id].chart;
  return (
    <span
      aria-hidden="true"
      className="flex h-16 overflow-hidden rounded-lg"
      style={{ background: p.bg, border: `1px solid ${p.border}` }}
    >
      <span
        className="flex w-3.5 flex-col items-center gap-1 pt-1.5"
        style={{ background: p.rail, borderRight: `1px solid ${p.railBorder}` }}
      >
        <span className="h-1.5 w-1.5 rounded-sm" style={{ background: p.accent }} />
        <span className="h-[3px] w-1.5 rounded-sm" style={{ background: p.railMuted }} />
        <span className="h-[3px] w-1.5 rounded-sm" style={{ background: p.railMuted }} />
      </span>
      <span className="flex min-w-0 flex-1 flex-col">
        <span
          className="h-2.5"
          style={{ background: p.rail, borderBottom: `1px solid ${p.railBorder}` }}
        />
        <span className="flex flex-1 gap-1 p-1.5">
          <span
            className="flex flex-1 items-end gap-0.5 rounded p-1"
            style={{ background: p.panel, border: `1px solid ${p.border}` }}
          >
            {[
              [40, s0],
              [70, s0],
              [55, s1],
              [85, s0],
            ].map(([height, color], i) => (
              <span
                key={i}
                className="flex-1 rounded-[1px]"
                style={{ height: `${height}%`, background: color }}
              />
            ))}
          </span>
          <span
            className="flex flex-1 flex-col gap-[3px] rounded p-1"
            style={{ background: p.panel, border: `1px solid ${p.border}` }}
          >
            <span className="h-[3px] w-4/5 rounded-sm opacity-50" style={{ background: p.muted }} />
            <span className="h-[3px] w-3/5 rounded-sm opacity-50" style={{ background: p.muted }} />
            <span className="mt-auto h-1 w-2/5 rounded-sm" style={{ background: p.accent }} />
          </span>
        </span>
      </span>
    </span>
  );
};

const ThemeMenuContent = (): JSX.Element => {
  const {
    prefs,
    updatePrefs,
    resetPrefs,
    themeId,
    linkOverride,
    clearLinkOverride,
    userChoiceAllowed,
    savedToProfile,
  } = useTheme();
  const isSystem = prefs.mode === 'system';

  const pick = (id: ThemeId) => {
    const scheme = THEMES[id].scheme;
    const patch: Partial<typeof prefs> = {};
    if (scheme === 'light') patch.light = id as typeof prefs.light;
    else patch.dark = id as typeof prefs.dark;
    if (!isSystem) patch.mode = scheme;
    updatePrefs(patch);
  };

  const modeNote = userChoiceAllowed
    ? isSystem
      ? 'Follows your computer’s light/dark setting, using one light and one dark theme.'
      : prefs.mode === 'light'
        ? 'Always the light theme you pick below.'
        : 'Always the dark theme you pick below.'
    : isSystem
      ? 'Follows your computer’s light/dark setting.'
      : prefs.mode === 'light'
        ? 'Always the light theme.'
        : 'Always the dark theme.';

  const renderCard = (id: ThemeId) => {
    const theme = THEMES[id];
    const chosen = theme.scheme === 'light' ? prefs.light === id : prefs.dark === id;
    const showing = id === themeId;
    const tag = isSystem ? (chosen ? (theme.scheme === 'light' ? 'Day' : 'Night') : '') : '';
    const selected = isSystem ? chosen : showing;
    return (
      <button
        key={id}
        type="button"
        aria-pressed={selected}
        title={theme.description}
        onClick={() => pick(id)}
        className={`flex min-w-0 cursor-pointer flex-col gap-1.5 rounded-xl border-2 p-2 text-left focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-[var(--v2-accent)] ${
          showing
            ? 'border-[var(--v2-accent)] bg-[var(--v2-ok-soft)]'
            : selected
              ? 'border-[var(--v2-accent)]/50 bg-transparent'
              : 'border-gray-200 bg-transparent hover:border-gray-300 dark:border-gray-700 dark:hover:border-gray-600'
        }`}
      >
        <ThemePreview id={id} />
        <span className="flex items-center justify-between gap-1">
          <span className="truncate text-[13px] font-semibold text-gray-900 dark:text-gray-50">
            {theme.name}
          </span>
          {tag && (
            <span className="rounded-full bg-[var(--v2-ok-soft)] px-1.5 text-[11px] font-medium text-[var(--v2-accent)]">
              {tag}
            </span>
          )}
        </span>
        <span className="text-xs leading-tight text-gray-500 dark:text-gray-400">
          {theme.description}
        </span>
      </button>
    );
  };

  return (
    <div className="flex w-[440px] max-w-[calc(100vw-32px)] flex-col gap-3.5 p-1">
      <div>
        <div className="text-base font-semibold text-gray-900 dark:text-gray-50">Theme</div>
        <div className="text-xs text-gray-500 dark:text-gray-400">
          {savedToProfile
            ? 'Applies instantly · saved to your profile, so it follows you to any browser.'
            : 'Applies instantly and is saved in this browser.'}
        </div>
      </div>

      {linkOverride && (
        <Alert
          type="info"
          showIcon
          message={`This screen uses ${THEMES[linkOverride].name} from its link (?theme=).`}
          action={
            <Button size="small" onClick={clearLinkOverride}>
              Use my theme
            </Button>
          }
        />
      )}

      <div className="flex flex-col gap-1.5">
        <span className="text-xs text-gray-500 dark:text-gray-400">Mode</span>
        <Segmented<ThemePreference>
          value={prefs.mode}
          onChange={(mode) => updatePrefs({ mode })}
          options={[
            { value: 'light', label: 'Light', icon: <SunOutlined /> },
            { value: 'dark', label: 'Dark', icon: <MoonOutlined /> },
            { value: 'system', label: 'System', icon: <DesktopOutlined /> },
          ]}
          className="self-start"
        />
        <span className="text-xs text-gray-500 dark:text-gray-400">{modeNote}</span>
      </div>

      {!userChoiceAllowed && (
        <span className="text-xs text-gray-500 dark:text-gray-400">
          Your admin has set the theme for everyone.
        </span>
      )}

      {userChoiceAllowed &&
        [
          { label: 'Light themes', ids: LIGHT_THEME_IDS, hint: 'used when your system is light' },
          { label: 'Dark themes', ids: DARK_THEME_IDS, hint: 'used when your system is dark' },
        ].map((group) => (
          <div key={group.label} className="flex flex-col gap-2">
            <span className="flex justify-between gap-2 text-xs text-gray-500 dark:text-gray-400">
              <span>{group.label}</span>
              {isSystem && <span>{group.hint}</span>}
            </span>
            <div className="grid grid-cols-3 gap-2">{group.ids.map(renderCard)}</div>
          </div>
        ))}

      <div className="flex items-center justify-between gap-3 rounded-xl border border-gray-200 bg-gray-50 px-3 py-2.5 dark:border-gray-700 dark:bg-gray-900">
        <span className="flex min-w-0 flex-col gap-1.5">
          <span className="text-[13px] font-medium text-gray-900 dark:text-gray-50">
            Colour-blind safe chart colours
          </span>
          <span className="flex gap-1" aria-hidden="true">
            {COLOR_BLIND_SWATCHES.map((color) => (
              <span
                key={color}
                className="h-2.5 w-2.5 rounded-[3px]"
                style={{ background: color }}
              />
            ))}
          </span>
        </span>
        <Switch
          aria-label="Colour-blind safe chart colours"
          checked={prefs.colorBlindCharts}
          onChange={(colorBlindCharts) => updatePrefs({ colorBlindCharts })}
        />
      </div>

      <div className="flex flex-wrap items-center justify-between gap-2 border-t border-gray-200 pt-2.5 dark:border-gray-700">
        <span className="flex flex-col gap-1 text-xs text-gray-500 dark:text-gray-400">
          <span>Status colours (healthy, warning, critical) are the same in every theme.</span>
          {userChoiceAllowed && (
            <span>Your admin sets the default in Obzly settings › General › Appearance.</span>
          )}
        </span>
        <Button size="small" onClick={resetPrefs}>
          Reset to default
        </Button>
      </div>
    </div>
  );
};

/** Dark Ant Design tokens for the top bar in Semi-dark, so its pickers match the dark bar. */
export const DarkChromeProvider = ({
  enabled,
  children,
}: {
  enabled: boolean;
  children: ReactNode;
}): JSX.Element =>
  enabled ? (
    <ConfigProvider
      theme={{ algorithm: antdTheme.darkAlgorithm, token: THEMES['emerald-dark'].antd }}
    >
      {children}
    </ConfigProvider>
  ) : (
    <>{children}</>
  );

/** The top-bar theme button: same icon button as before, now opening the theme menu. */
export const ThemeMenuComponent = (): JSX.Element => {
  const { theme } = useTheme();
  const [open, setOpen] = useState(false);

  return (
    <Popover
      trigger="click"
      placement="bottomRight"
      open={open}
      onOpenChange={setOpen}
      content={<ThemeMenuContent />}
      arrow={false}
    >
      <Tooltip title={open ? undefined : 'Theme'}>
        <Button
          type="text"
          shape="circle"
          aria-label="Theme"
          aria-haspopup="dialog"
          aria-expanded={open}
          icon={theme === 'dark' ? <SunOutlined /> : <MoonOutlined />}
        />
      </Tooltip>
    </Popover>
  );
};
