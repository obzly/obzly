import { ToolOutlined } from '@ant-design/icons';
import { Button, Tooltip } from 'antd';
import { type ReactNode, memo, useMemo, useState } from 'react';

import type { CheckType } from '../../../entity/synthetics';
import { MAINTENANCE_HATCH, maintenanceRuns } from '../lib/maintenance';
import { type MonitorStatus, STATUS_COLOR, STATUS_LABEL } from '../lib/status';
import { slotAt, stripBackground, stripSummary } from '../lib/stripPaint';
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
  <span className="rounded border border-gray-300 px-1.5 py-px font-mono text-[11px] tracking-wide text-gray-500 uppercase dark:border-gray-600 dark:text-gray-400">
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

/**
 * Availability over the range, one square per slot; the hovered one says when
 * and how. Painted as one background so a page of strips stays cheap: only
 * the hovered slot gets an element (the tooltip anchor) and a label.
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
  const background = useMemo(() => stripBackground(slots), [slots]);
  // Hatching can't be one stop of the single gradient: maintenance runs get
  // an element each (rare, unlike slots).
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
      className="relative h-[18px] [--strip-empty:#e5e7eb] dark:[--strip-empty:#374151]"
      style={{ background }}
      role="img"
      aria-label={`Availability: ${stripSummary(slots)}`}
      onMouseMove={(event) => {
        const box = event.currentTarget.getBoundingClientRect();
        const index = slotAt(event.clientX - box.left, box.width, slots.length);
        if (index !== hovered) setHovered(index);
      }}
      onMouseLeave={() => setHovered(undefined)}
    >
      {hatched.map((run) => (
        <i
          key={run.start}
          className="pointer-events-none absolute top-0 block h-full rounded-sm"
          style={{
            left: `${(run.start * 100) / slots.length}%`,
            width: `calc(${(run.length * 100) / slots.length}% - 2px)`,
            background: MAINTENANCE_HATCH,
          }}
        />
      ))}
      {hovered !== undefined && (
        <Tooltip open title={label(hovered)} placement="top">
          <i
            className="pointer-events-none absolute top-0 block h-full rounded-sm outline outline-1 outline-gray-500/60"
            style={{
              left: `${(hovered * 100) / slots.length}%`,
              width: `calc(${100 / slots.length}% - 2px)`,
            }}
          />
        </Tooltip>
      )}
    </div>
  );
};

/** Memoised: a page of strips re-renders only those whose slots changed. */
export const Strip = memo(StripView);

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
  <div
    className={`min-w-0 rounded-lg border border-gray-200 bg-white p-4 dark:border-gray-700 dark:bg-gray-800 ${className}`}
  >
    <div className="mb-2 flex flex-wrap items-baseline justify-between gap-2">
      <div>
        <div className="text-sm font-semibold text-gray-900 dark:text-gray-100">{title}</div>
        {subtitle && <div className="text-xs text-gray-500 dark:text-gray-400">{subtitle}</div>}
      </div>
      {extra}
    </div>
    {children}
  </div>
);
