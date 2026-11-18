// The themes a user can pick from the top-bar theme menu. CSS colours for each
// one live in index.css under html[data-theme="<id>"]; this file holds what
// JavaScript needs (Ant Design tokens, chart colours, the menu's previews).

export type ColorScheme = 'light' | 'dark';

export const LIGHT_THEME_IDS = ['emerald-light', 'soft-light', 'semi-dark'] as const;
export const DARK_THEME_IDS = ['emerald-dark', 'midnight', 'black'] as const;

export type LightThemeId = (typeof LIGHT_THEME_IDS)[number];
export type DarkThemeId = (typeof DARK_THEME_IDS)[number];
export type ThemeId = LightThemeId | DarkThemeId;

/** Status colours never change with the theme: a red cell means the same on every screen. */
export interface StatusColors {
  ok: string;
  warn: string;
  bad: string;
  info: string;
  noData: string;
}

export interface ThemeDefinition {
  id: ThemeId;
  name: string;
  description: string;
  scheme: ColorScheme;
  /** Semi-dark keeps light pages but draws the rail and top bar dark. */
  darkChrome: boolean;
  antd: {
    colorPrimary: string;
    colorBgLayout: string;
    colorBgContainer: string;
    colorBgElevated: string;
    colorBorder: string;
    colorBorderSecondary: string;
  };
  /** Series colours for charts, in order. */
  chart: string[];
  /** Page colours for code that can't use CSS classes (ECharts axes, grids, tooltips). Mirrors the --v2-* variables. */
  colors: {
    bg: string;
    panel: string;
    panel2: string;
    border: string;
    text: string;
    muted: string;
    track: string;
    accent: string;
  };
  preview: {
    bg: string;
    panel: string;
    border: string;
    muted: string;
    accent: string;
    rail: string;
    railBorder: string;
    railMuted: string;
  };
}

const LIGHT_STATUS: StatusColors = {
  ok: '#059669',
  warn: '#d97706',
  bad: '#dc2626',
  info: '#2563eb',
  noData: '#cfd8d4',
};
const DARK_STATUS: StatusColors = {
  ok: '#10b981',
  warn: '#f59e0b',
  bad: '#f87171',
  info: '#60a5fa',
  noData: '#33433d',
};

const EMERALD_LIGHT_CHART = ['#059669', '#2563eb', '#7c3aed', '#d97706', '#db2777', '#0891b2'];
const EMERALD_DARK_CHART = ['#10b981', '#60a5fa', '#a78bfa', '#f59e0b', '#f472b6', '#22d3ee'];

// Okabe-Ito: distinguishable with the common colour-vision deficiencies.
const COLOR_BLIND_LIGHT = ['#0072B2', '#E69F00', '#009E73', '#CC79A7', '#D55E00', '#56B4E9'];
const COLOR_BLIND_DARK = ['#56B4E9', '#E69F00', '#009E73', '#CC79A7', '#F0E442', '#D55E00'];

const emeraldLightAntd = {
  colorPrimary: '#009966',
  colorBgLayout: '#f3f6f5',
  colorBgContainer: '#ffffff',
  colorBgElevated: '#ffffff',
  colorBorder: '#d5ded9',
  colorBorderSecondary: '#e0e7e4',
};
const emeraldLightPreview = {
  bg: '#f3f6f5',
  panel: '#ffffff',
  border: '#e0e7e4',
  muted: '#55665f',
  accent: '#009966',
  rail: '#ffffff',
  railBorder: '#e0e7e4',
  railMuted: '#9aaba4',
};

export const THEMES: Record<ThemeId, ThemeDefinition> = {
  'emerald-light': {
    id: 'emerald-light',
    name: 'Emerald light',
    description: 'The standard light look',
    scheme: 'light',
    darkChrome: false,
    antd: emeraldLightAntd,
    chart: EMERALD_LIGHT_CHART,
    colors: {
      bg: '#f3f6f5',
      panel: '#ffffff',
      panel2: '#f2f6f4',
      border: '#e0e7e4',
      text: '#0f1a16',
      muted: '#55665f',
      track: '#e6ece9',
      accent: '#009966',
    },
    preview: emeraldLightPreview,
  },
  'soft-light': {
    id: 'soft-light',
    name: 'Soft light',
    description: 'Pale blue-grey, indigo accent',
    scheme: 'light',
    darkChrome: false,
    antd: {
      colorPrimary: '#4f46e5',
      colorBgLayout: '#f4f5fb',
      colorBgContainer: '#ffffff',
      colorBgElevated: '#ffffff',
      colorBorder: '#d6d9ea',
      colorBorderSecondary: '#e3e5f1',
    },
    chart: ['#4f46e5', '#0891b2', '#c026d3', '#d97706', '#059669', '#db2777'],
    colors: {
      bg: '#f4f5fb',
      panel: '#ffffff',
      panel2: '#f3f4fb',
      border: '#e3e5f1',
      text: '#171a2e',
      muted: '#5d6280',
      track: '#e8eaf5',
      accent: '#4f46e5',
    },
    preview: {
      bg: '#f4f5fb',
      panel: '#ffffff',
      border: '#e3e5f1',
      muted: '#5d6280',
      accent: '#4f46e5',
      rail: '#ffffff',
      railBorder: '#e3e5f1',
      railMuted: '#9ca2bd',
    },
  },
  'semi-dark': {
    id: 'semi-dark',
    name: 'Semi-dark',
    description: 'Dark rail and top bar, light pages',
    scheme: 'light',
    darkChrome: true,
    antd: emeraldLightAntd,
    chart: EMERALD_LIGHT_CHART,
    colors: {
      bg: '#f3f6f5',
      panel: '#ffffff',
      panel2: '#f2f6f4',
      border: '#e0e7e4',
      text: '#0f1a16',
      muted: '#55665f',
      track: '#e6ece9',
      accent: '#009966',
    },
    preview: {
      ...emeraldLightPreview,
      rail: '#0d1714',
      railBorder: '#21312b',
      railMuted: '#9aaba4',
    },
  },
  'emerald-dark': {
    id: 'emerald-dark',
    name: 'Emerald dark',
    description: 'The standard dark look',
    scheme: 'dark',
    darkChrome: true,
    antd: {
      colorPrimary: '#10b981',
      colorBgLayout: '#0a1210',
      colorBgContainer: '#111c18',
      colorBgElevated: '#17241f',
      colorBorder: '#26352f',
      colorBorderSecondary: '#21312b',
    },
    chart: EMERALD_DARK_CHART,
    colors: {
      bg: '#0a1210',
      panel: '#111c18',
      panel2: '#17241f',
      border: '#21312b',
      text: '#e8f0ec',
      muted: '#9aaba4',
      track: '#1d2a25',
      accent: '#10b981',
    },
    preview: {
      bg: '#0a1210',
      panel: '#111c18',
      border: '#21312b',
      muted: '#9aaba4',
      accent: '#10b981',
      rail: '#111c18',
      railBorder: '#21312b',
      railMuted: '#6b7d76',
    },
  },
  midnight: {
    id: 'midnight',
    name: 'Midnight',
    description: 'Deep navy, blue accent',
    scheme: 'dark',
    darkChrome: true,
    antd: {
      colorPrimary: '#5b8cff',
      colorBgLayout: '#0a0f1e',
      colorBgContainer: '#111a30',
      colorBgElevated: '#17213b',
      colorBorder: '#2a3555',
      colorBorderSecondary: '#232e4b',
    },
    chart: ['#5b8cff', '#2dd4bf', '#c084fc', '#fbbf24', '#f472b6', '#38bdf8'],
    colors: {
      bg: '#0a0f1e',
      panel: '#111a30',
      panel2: '#17213b',
      border: '#232e4b',
      text: '#e6ebf7',
      muted: '#95a1c0',
      track: '#1b2542',
      accent: '#5b8cff',
    },
    preview: {
      bg: '#0a0f1e',
      panel: '#111a30',
      border: '#232e4b',
      muted: '#95a1c0',
      accent: '#5b8cff',
      rail: '#111a30',
      railBorder: '#232e4b',
      railMuted: '#6b7896',
    },
  },
  black: {
    id: 'black',
    name: 'Black',
    description: 'True black for wall screens and OLED',
    scheme: 'dark',
    darkChrome: true,
    antd: {
      colorPrimary: '#10b981',
      colorBgLayout: '#000000',
      colorBgContainer: '#0b0b0c',
      colorBgElevated: '#141416',
      colorBorder: '#27272b',
      colorBorderSecondary: '#1f1f23',
    },
    chart: EMERALD_DARK_CHART,
    colors: {
      bg: '#000000',
      panel: '#0b0b0c',
      panel2: '#141416',
      border: '#1f1f23',
      text: '#ededef',
      muted: '#9b9ba3',
      track: '#19191c',
      accent: '#10b981',
    },
    preview: {
      bg: '#000000',
      panel: '#0b0b0c',
      border: '#1f1f23',
      muted: '#9b9ba3',
      accent: '#10b981',
      rail: '#0b0b0c',
      railBorder: '#1f1f23',
      railMuted: '#6e6e76',
    },
  },
};

export const isThemeId = (value: unknown): value is ThemeId =>
  typeof value === 'string' && Object.prototype.hasOwnProperty.call(THEMES, value);

export const isLightThemeId = (value: unknown): value is LightThemeId =>
  typeof value === 'string' && (LIGHT_THEME_IDS as readonly string[]).includes(value);

export const isDarkThemeId = (value: unknown): value is DarkThemeId =>
  typeof value === 'string' && (DARK_THEME_IDS as readonly string[]).includes(value);

export const statusColors = (scheme: ColorScheme): StatusColors =>
  scheme === 'dark' ? DARK_STATUS : LIGHT_STATUS;

export const chartPalette = (theme: ThemeDefinition, colorBlindSafe: boolean): string[] => {
  if (!colorBlindSafe) return theme.chart;
  return theme.scheme === 'dark' ? COLOR_BLIND_DARK : COLOR_BLIND_LIGHT;
};
