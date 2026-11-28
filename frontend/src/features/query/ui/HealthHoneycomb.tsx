import React, { useRef, useState } from 'react';

import type { HealthGroup, HealthTile } from '../../../entity/query';
import { HEX_GAP, fitRadius, groupBox, hexScale } from '../lib/healthHoneycomb';
import { formatCount } from '../lib/logInsights';

interface Props {
  groups: HealthGroup[];
  /** The value tiles are coloured and ordered by (worst first); null when unknown. */
  metric: (tile: HealthTile) => number | null;
  color: (tile: HealthTile) => string;
  metricText: (tile: HealthTile) => string;
  selected: { group: string; tile: string } | null;
  selectedColor: string;
  onSelect: (group: string, tile: string) => void;
  /** The room to fill; hexagons grow until the groups would no longer fit. */
  width: number;
  height: number;
}

const SQRT3 = Math.sqrt(3);

const hexPoints = (cx: number, cy: number, r: number) =>
  Array.from({ length: 6 }, (_, i) => {
    const angle = (Math.PI / 180) * (60 * i - 30);
    return `${(cx + r * Math.cos(angle)).toFixed(1)},${(cy + r * Math.sin(angle)).toFixed(1)}`;
  }).join(' ');

const TOOLTIP_WIDTH = 240;

/**
 * Health map as a honeycomb: one slot per workload, grouped by namespace (or
 * host), so a hundred small workloads stay as findable as one big one. The
 * hexagon in each slot is sized by log volume against the namespace's busiest
 * workload (hexScale); hovering one names it.
 */
export const HealthHoneycomb = ({
  groups,
  metric,
  color,
  metricText,
  selected,
  selectedColor,
  onSelect,
  width,
  height,
}: Props): React.JSX.Element => {
  const r = fitRadius(
    groups.map((g) => g.tiles.length),
    width,
    height,
  );
  const w = SQRT3 * r;
  const containerRef = useRef<HTMLDivElement>(null);
  const [hover, setHover] = useState<{ group: string; tile: HealthTile; x: number; y: number }>();
  const allLines = groups.reduce((sum, g) => sum + g.total, 0);
  const track = (event: React.MouseEvent, group: string, tile: HealthTile) => {
    const box = containerRef.current?.getBoundingClientRect();
    if (!box || !containerRef.current) return;
    const x = event.clientX - box.left;
    setHover({
      group,
      tile,
      x: x + 14 + TOOLTIP_WIDTH > box.width ? Math.max(0, x - 14 - TOOLTIP_WIDTH) : x + 14,
      y: event.clientY - box.top + containerRef.current.scrollTop + 14,
    });
  };
  const worstFirst = (a: HealthTile, b: HealthTile) =>
    (metric(b) ?? -Infinity) - (metric(a) ?? -Infinity) || b.total - a.total;

  return (
    <div
      ref={containerRef}
      className="relative flex flex-wrap content-start gap-x-5 gap-y-3 overflow-y-auto"
      style={{ height }}
      onMouseLeave={() => setHover(undefined)}
    >
      {groups.map((group) => {
        const tiles = [...group.tiles].sort(worstFirst);
        const box = groupBox(tiles.length, r);
        // Sized within the namespace: one busy workload elsewhere would shrink all the rest.
        const groupMost = Math.max(0, ...tiles.map((t) => t.total));
        return (
          <figure key={group.name} className="m-0 flex min-w-0 flex-col gap-1">
            <figcaption
              className="truncate font-mono text-xs text-gray-500 dark:text-gray-400"
              style={{ width: box.width }}
              title={`${group.name} · ${group.total.toLocaleString()} lines`}
            >
              {group.name} · {formatCount(group.total)}
            </figcaption>
            <svg
              width={box.svgWidth}
              height={box.svgHeight}
              viewBox={`0 0 ${box.svgWidth} ${box.svgHeight}`}
              role="group"
              aria-label={`${group.name}: ${tiles.length} ${tiles.length === 1 ? 'tile' : 'tiles'}`}
            >
              {tiles.map((tile, i) => {
                const row = Math.floor(i / box.columns);
                const col = i % box.columns;
                const cx = HEX_GAP / 2 + w / 2 + col * w + (row % 2 ? w / 2 : 0);
                const cy = HEX_GAP / 2 + r + row * 1.5 * r;
                const isSelected = selected?.group === group.name && selected?.tile === tile.name;
                const label = `${tile.name}: ${tile.total.toLocaleString()} lines, ${metricText(tile)}`;
                return (
                  <g
                    key={tile.name}
                    role="button"
                    tabIndex={0}
                    aria-label={label}
                    aria-pressed={isSelected}
                    className="cursor-pointer outline-none focus-visible:opacity-80"
                    onClick={() => onSelect(group.name, tile.name)}
                    onMouseEnter={(event) => track(event, group.name, tile)}
                    onMouseMove={(event) => track(event, group.name, tile)}
                    onMouseLeave={() => setHover(undefined)}
                    onKeyDown={(event) => {
                      if (event.key === 'Enter' || event.key === ' ') {
                        event.preventDefault();
                        onSelect(group.name, tile.name);
                      }
                    }}
                  >
                    {/* The whole slot: faint, and what the pointer hits. */}
                    <polygon
                      points={hexPoints(cx, cy, r - HEX_GAP / 2)}
                      fill="currentColor"
                      className="text-gray-400/15 dark:text-gray-500/20"
                      stroke={isSelected ? selectedColor : 'none'}
                      strokeWidth={isSelected ? 2 : 0}
                    />
                    <polygon
                      points={hexPoints(
                        cx,
                        cy,
                        (r - HEX_GAP / 2) * hexScale(tile.total, groupMost),
                      )}
                      fill={color(tile)}
                      className="transition-opacity hover:opacity-80"
                    />
                  </g>
                );
              })}
            </svg>
          </figure>
        );
      })}
      {hover && (
        <div
          role="tooltip"
          className="pointer-events-none absolute z-10 flex flex-col gap-0.5 rounded-lg bg-gray-900/95 px-2.5 py-2 text-xs text-white shadow-lg"
          style={{ left: hover.x, top: hover.y, width: TOOLTIP_WIDTH }}
        >
          <span className="font-mono font-semibold break-all">{hover.tile.name}</span>
          <span className="font-mono text-gray-300">{hover.group}</span>
          <span>
            {hover.tile.total.toLocaleString()} lines
            {allLines > 0 && ` · ${((hover.tile.total / allLines) * 100).toFixed(1)}% of all`}
          </span>
          <span>{metricText(hover.tile)}</span>
        </div>
      )}
    </div>
  );
};
