import type { StatusColors } from '../../../shared/theme';

/** Status class colours: success, redirect, client error, server error. */
export const statusColor = (code: string, status: StatusColors): string => {
  switch (code[0]) {
    case '2':
      return status.ok;
    case '3':
      return status.info;
    case '4':
      return status.warn;
    case '5':
      return status.bad;
    default:
      return status.noData;
  }
};

/** "#10b981" with an alpha, for tints and ramps that follow the accent. */
export const withAlpha = (hex: string, alpha: number): string => {
  const value = hex.replace('#', '');
  if (!/^[0-9a-fA-F]{6}$/.test(value)) return hex;
  const n = parseInt(value, 16);
  return `rgba(${n >> 16}, ${(n >> 8) & 255}, ${n & 255}, ${alpha})`;
};
