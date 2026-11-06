import { STATUS_COLOR, STATUS_LABEL } from './status';
import type { SlotStatus } from './strip';

export const SLOT_COLOR: Record<SlotStatus, string> = {
  up: STATUS_COLOR.up,
  warn: STATUS_COLOR.warn,
  down: STATUS_COLOR.down,
  maintenance: STATUS_COLOR.maintenance,
  none: 'var(--strip-empty, #e5e7eb)',
};

/**
 * One CSS background painting every slot with a 2px gap, so a strip is a
 * single element instead of one per slot (thousands on a big Overview).
 */
export const stripBackground = (slots: SlotStatus[]): string => {
  if (slots.length === 0) return 'none';
  const width = 100 / slots.length;
  const stops = slots.map((slot, index) => {
    const start = index * width;
    const end = start + width;
    const last = index === slots.length - 1;
    return `${SLOT_COLOR[slot]} ${start}% ${last ? `${end}%` : `calc(${end}% - 2px)`}, transparent 0 ${end}%`;
  });
  return `linear-gradient(90deg, ${stops.join(', ')})`;
};

/** Which slot a pointer at offsetX over a strip of the given width is on. */
export const slotAt = (offsetX: number, width: number, count: number): number | undefined => {
  if (width <= 0 || count === 0 || offsetX < 0 || offsetX >= width) return undefined;
  return Math.min(count - 1, Math.floor((offsetX / width) * count));
};

/** Screen-reader summary, e.g. "45 up, 2 down, 1 no runs". */
export const stripSummary = (slots: SlotStatus[]): string => {
  const counts = new Map<SlotStatus, number>();
  for (const slot of slots) counts.set(slot, (counts.get(slot) ?? 0) + 1);
  return (['up', 'warn', 'down', 'maintenance', 'none'] as SlotStatus[])
    .filter((slot) => counts.get(slot))
    .map((slot) => `${counts.get(slot)} ${slot === 'none' ? 'no runs' : STATUS_LABEL[slot]}`)
    .join(', ');
};
