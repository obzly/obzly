import { ToolOutlined } from '@ant-design/icons';
import { Button, Tooltip } from 'antd';
import { type ReactNode, memo, useMemo, useState } from 'react';

import type { CheckType } from '../../../entity/synthetics';
import { Card, StatusStrip, type StripState } from '../../../shared/ui/v2';
import { MAINTENANCE_HATCH, maintenanceRuns } from '../lib/maintenance';
import { type MonitorStatus, STATUS_COLOR, STATUS_LABEL } from '../lib/status';
import { slotAt, stripSummary } from '../lib/stripPaint';
import type { SlotStatus } from '../lib/useMonitorData';

// Tooltips open above what they describe, never under the pointer as a
// browser `title` does.
export const StatusDot = ({
  status,
  size = 10,
  tooltip = false,
}: {
  status: MonitorStatus | SlotStatus;
  size?: number;
  /** Name the status on hover, for a dot shown without a label. */
  tooltip?: boolean;
}) => {
  const dot =
    status === 'maintenance' ? (
      <ToolOutlined
        role="img"
        aria-label={STATUS_LABEL[status]}
        className="shrink-0"
        style={{ color: STATUS_COLOR[status], fontSize: size + 2 }}
      />
    ) : (
      <span
        className="inline-block shrink-0 rounded-full"
        style={{
          width: size,
          height: size,
          // Impacted: half-filled amber, so it reads apart from Degraded.
          background:
            status === 'impacted'
              ? `linear-gradient(90deg, ${STATUS_COLOR.impacted} 50%, transparent 50%)`
              : STATUS_COLOR[status],
          boxShadow:
            status === 'impacted' ? `inset 0 0 0 1.5px ${STATUS_COLOR.impacted}` : undefined,
        }}
        role="img"
        aria-label={STATUS_LABEL[status]}
      />
    );
  return tooltip ? (
    <Tooltip title={STATUS_LABEL[status]} placement="top">
      {dot}
    </Tooltip>
  ) : (
    dot
  );
};

export const StatusBadge = ({ status, suffix }: { status: MonitorStatus; suffix?: string }) => (
  <span
    className="inline-flex items-center gap-1.5 rounded-full px-2 py-0.5 text-xs font-semibold whitespace-nowrap"
    style={{ color: STATUS_COLOR[status], background: `${STATUS_COLOR[status]}1f` }}
  >
    <StatusDot status={status} size={8} />
    {STATUS_LABEL[status]}
    {suffix && ` ${suffix}`}
  </span>
);

export const TypeTag = ({ type }: { type: CheckType }) => (
  <span className="rounded-full bg-gray-100 px-2 py-0.5 font-mono text-[11px] tracking-wide text-gray-600 uppercase dark:bg-gray-700 dark:text-gray-300">
    {type}
  </span>
);

/** An icon-only action with its name as a tooltip (and aria-label). */
export const IconAction = ({
  title,
  icon,
  onClick,
  danger,
  disabled,
  loading,
}: {
  title: string;
  icon: ReactNode;
  onClick?: () => void;
  danger?: boolean;
  disabled?: boolean;
  loading?: boolean;
}) => (
  <Tooltip title={title}>
    <Button
      type="text"
      size="small"
      icon={icon}
      aria-label={title}
      danger={danger}
      disabled={disabled}
      loading={loading}
      onClick={
        // Without onClick the click must reach a Popconfirm wrapping the button.
        onClick &&
        ((event) => {
          event.stopPropagation();
          onClick();
        })
      }
    />
  </Tooltip>
);

const slotTime = new Intl.DateTimeFormat(undefined, {
  day: 'numeric',
  month: 'short',
  hour: '2-digit',
  minute: '2-digit',
});
const slotEnd = new Intl.DateTimeFormat(undefined, { hour: '2-digit', minute: '2-digit' });

// No runs (and maintenance, hatched on top) stay grey, never green.
const STRIP_STATE: Record<SlotStatus, StripState> = {
  up: 'ok',
  warn: 'warn',
  down: 'fail',
  maintenance: 'none',
  none: 'none',
};

// Where slot `index` (or a run of `length` slots) sits on a StatusStrip with 2px gaps.
const slotLeft = (index: number, count: number) =>
  `calc((100% - ${(count - 1) * 2}px) * ${index / count} + ${index * 2}px)`;
const slotWidth = (length: number, count: number) =>
  `calc((100% - ${(count - 1) * 2}px) * ${length / count} + ${(length - 1) * 2}px)`;

/**
 * Availability over the range, one cell per slot (the shared StatusStrip);
 * the hovered one says when and how in a tooltip above it, never a browser
 * `title` under the pointer.
 */
const StripView = ({
  slots,
  slotSeconds,
  from,
}: {
  slots: SlotStatus[];
  slotSeconds: number;
  from: number;
}) => {
  const [hovered, setHovered] = useState<number>();
  const cells = useMemo(() => slots.map((slot) => ({ state: STRIP_STATE[slot] })), [slots]);
  // Maintenance runs get a hatched element each (rare, unlike slots).
  const hatched = useMemo(() => maintenanceRuns(slots), [slots]);
  const label = (index: number) => {
    const start = from + index * slotSeconds * 1000;
    const slot = slots[index];
    const text =
      slot === 'none'
        ? 'no runs'
        : slot === 'maintenance'
          ? 'Maintenance (not counted)'
          : STATUS_LABEL[slot];
    return `${slotTime.format(start)} – ${slotEnd.format(start + slotSeconds * 1000)}: ${text}`;
  };
  return (
    <div
      className="relative"
      onMouseMove={(event) => {
        const box = event.currentTarget.getBoundingClientRect();
        const index = slotAt(event.clientX - box.left, box.width, slots.length);
        if (index !== hovered) setHovered(index);
      }}
      onMouseLeave={() => setHovered(undefined)}
    >
      <StatusStrip cells={cells} height={18} label={`Availability: ${stripSummary(slots)}`} />
      {hatched.map((run) => (
        <i
          key={run.start}
          className="pointer-events-none absolute top-0 block h-full rounded-sm"
          style={{
            left: slotLeft(run.start, slots.length),
            width: slotWidth(run.length, slots.length),
            background: MAINTENANCE_HATCH,
          }}
        />
      ))}
      {hovered !== undefined && (
        <Tooltip open title={label(hovered)} placement="top">
          <i
            className="pointer-events-none absolute top-0 block h-full rounded-sm outline outline-1 outline-gray-500/60"
            style={{
              left: slotLeft(hovered, slots.length),
              width: slotWidth(1, slots.length),
            }}
          />
        </Tooltip>
      )}
    </div>
  );
};

/** Memoised: a page of strips re-renders only those whose slots changed. */
export const Strip = memo(StripView);

/** One location's latest result as a small pill: dot and name, the status on hover and for screen readers. */
export const LocationPill = ({ location, status }: { location: string; status: MonitorStatus }) => (
  <Tooltip title={`${location}: ${STATUS_LABEL[status]}`}>
    <span
      className="inline-flex items-center gap-1 rounded-full border border-gray-200 bg-gray-50 px-1.5 py-px font-mono text-[11px] text-gray-600 dark:border-gray-700 dark:bg-gray-900 dark:text-gray-300"
      aria-label={`${location}: ${STATUS_LABEL[status]}`}
    >
      <StatusDot status={status} size={7} />
      {location}
    </span>
  </Tooltip>
);

/** A v2 card with a title row; kept as Panel so Infrastructure and every Synthetics view share it. */
export const Panel = ({
  title,
  subtitle,
  extra,
  children,
  className = '',
}: {
  title: ReactNode;
  subtitle?: ReactNode;
  extra?: ReactNode;
  children: ReactNode;
  className?: string;
}) => (
  <Card title={title} subtitle={subtitle} extra={extra} className={className}>
    {children}
  </Card>
);
