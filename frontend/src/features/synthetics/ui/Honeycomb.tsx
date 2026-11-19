import { type ReactNode, memo, useEffect, useLayoutEffect, useRef, useState } from 'react';

import { halfHexPath, hexPath, layoutHexes } from '../lib/honeycomb';

export interface HoneycombItem {
  id: string;
  /** Read by screen readers: the monitor and what the colour means. */
  label: string;
  fill: string;
  /** Fill only the left half (impacted), the rest stays empty. */
  half?: boolean;
  /** An outline instead of a fill (disabled monitors). */
  outline?: string;
  dimmed?: boolean;
}

export interface HoneycombGroup {
  key: string;
  title?: ReactNode;
  items: HoneycombItem[];
}

interface Props {
  groups: HoneycombGroup[];
  radius: number;
  gap?: number;
  ariaLabel: string;
  /** Hover and focus content for one cell. */
  tooltip?: (id: string) => ReactNode;
  onOpen?: (id: string) => void;
  selectedId?: string;
}

const EMPTY = 'var(--v2-border)';
const TOOLTIP_WIDTH = 280;

/** Monitors as hexagon cells: compact enough for hundreds, readable for a dozen. */
const HoneycombView = ({
  groups,
  radius,
  gap = 2,
  ariaLabel,
  tooltip,
  onOpen,
  selectedId,
}: Props) => {
  const containerRef = useRef<HTMLDivElement>(null);
  const [width, setWidth] = useState(0);
  const [hover, setHover] = useState<{ id: string; left: number; top: number } | null>(null);

  useLayoutEffect(() => {
    const element = containerRef.current;
    if (!element) return;
    setWidth(element.clientWidth);
    const observer = new ResizeObserver(() => setWidth(element.clientWidth));
    observer.observe(element);
    return () => observer.disconnect();
  }, []);
  // A cell can vanish under the pointer when the data refreshes.
  useEffect(() => setHover(null), [groups]);

  const show = (id: string, target: Element) => {
    const container = containerRef.current;
    if (!tooltip || !container) return;
    const box = target.getBoundingClientRect();
    const outer = container.getBoundingClientRect();
    const left = Math.max(
      0,
      Math.min(
        box.left - outer.left + box.width / 2 - TOOLTIP_WIDTH / 2,
        outer.width - TOOLTIP_WIDTH,
      ),
    );
    setHover({ id, left, top: box.bottom - outer.top + 6 });
  };

  return (
    <div ref={containerRef} className="relative flex w-full min-w-0 flex-col gap-3">
      {width > 0 &&
        groups.map((group) => {
          const layout = layoutHexes(group.items.length, width, radius, gap);
          return (
            <div key={group.key} className="min-w-0">
              {group.title && (
                <div className="mb-1.5 text-xs font-semibold tracking-wide text-gray-500 uppercase dark:text-gray-400">
                  {group.title}
                </div>
              )}
              <svg
                width={width}
                height={layout.height}
                viewBox={`0 0 ${width} ${layout.height}`}
                role="group"
                aria-label={ariaLabel}
                className="block overflow-visible"
              >
                {group.items.map((item, index) => {
                  const { cx, cy } = layout.cells[index];
                  const d = hexPath(cx, cy, radius);
                  return (
                    <g
                      key={item.id}
                      role={onOpen ? 'button' : 'img'}
                      tabIndex={onOpen ? 0 : -1}
                      aria-label={item.label}
                      className={`transition-opacity outline-none focus-visible:[&>path:first-child]:stroke-[var(--v2-text)] focus-visible:[&>path:first-child]:stroke-2 ${onOpen ? 'cursor-pointer' : ''}`}
                      style={{ opacity: item.dimmed ? 0.18 : 1 }}
                      onMouseEnter={(event) => show(item.id, event.currentTarget)}
                      onMouseLeave={() => setHover(null)}
                      onFocus={(event) => show(item.id, event.currentTarget)}
                      onBlur={() => setHover(null)}
                      onClick={() => onOpen?.(item.id)}
                      onKeyDown={(event) => {
                        if (onOpen && (event.key === 'Enter' || event.key === ' ')) {
                          event.preventDefault();
                          onOpen(item.id);
                        }
                      }}
                    >
                      <path
                        d={d}
                        fill={item.half || item.outline ? EMPTY : item.fill}
                        stroke={item.outline ?? (item.half ? item.fill : 'none')}
                        strokeWidth={item.outline || item.half ? 1.5 : 0}
                      />
                      {item.half && <path d={halfHexPath(cx, cy, radius)} fill={item.fill} />}
                      {selectedId === item.id && (
                        <path
                          d={hexPath(cx, cy, radius + 2.5)}
                          fill="none"
                          stroke="var(--v2-text)"
                          strokeWidth={2}
                        />
                      )}
                    </g>
                  );
                })}
              </svg>
            </div>
          );
        })}
      {hover && tooltip && (
        <div
          role="tooltip"
          className="pointer-events-none absolute z-10 rounded-lg bg-gray-900 px-3 py-2.5 text-xs text-gray-100 shadow-xl dark:bg-gray-100 dark:text-gray-900"
          style={{ left: hover.left, top: hover.top, width: TOOLTIP_WIDTH }}
        >
          {tooltip(hover.id)}
        </div>
      )}
    </div>
  );
};

export const Honeycomb = memo(HoneycombView);
