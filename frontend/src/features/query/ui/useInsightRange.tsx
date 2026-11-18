import { ReloadOutlined } from '@ant-design/icons';
import { Button, Tooltip } from 'antd';
import dayjs from 'dayjs';
import React, { useMemo, useState } from 'react';

import { SegmentedControl } from './LogsViewTabs';
import type { TimeRange } from './TimeRangePickerComponent';

/**
 * A tab's own time range (last N), recomputed on choice or Refresh; the
 * control renders the choices plus a refresh button.
 */
export const useInsightRange = (
  options: { value: string; label: string; hours: number }[],
  initial: string,
): { range: TimeRange; control: React.JSX.Element; rangeText: string } => {
  const [choice, setChoice] = useState(initial);
  const [refreshToken, setRefreshToken] = useState(0);
  const selected = options.find((o) => o.value === choice) ?? options[0];
  const range = useMemo(() => {
    const to = dayjs().startOf('minute');
    return { from: to.subtract(selected.hours, 'hour'), to };
    // refreshToken: Refresh recomputes "now".
  }, [selected.hours, refreshToken]);
  const control = (
    <span className="inline-flex items-center gap-1.5">
      <SegmentedControl
        label="Time range"
        value={choice}
        options={options.map((o) => ({ value: o.value, label: o.value }))}
        onChange={setChoice}
      />
      <Tooltip title="Refresh">
        <Button
          size="small"
          type="text"
          aria-label="Refresh"
          icon={<ReloadOutlined />}
          onClick={() => setRefreshToken((t) => t + 1)}
        />
      </Tooltip>
    </span>
  );
  return { range, control, rangeText: selected.label.toLowerCase() };
};
