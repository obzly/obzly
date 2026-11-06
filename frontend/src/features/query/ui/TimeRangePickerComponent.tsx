import { ClockCircleOutlined } from '@ant-design/icons';
import { DatePicker, Select } from 'antd';
import dayjs from 'dayjs';
import React, { useEffect, useMemo, useState } from 'react';

import { getUserShortTimeFormat } from '../../../shared/time';

const { RangePicker } = DatePicker;

export interface TimeRange {
  from: dayjs.Dayjs;
  to: dayjs.Dayjs;
}

export interface TimeRangePreset {
  label: string;
  value: string;
  getRange: () => TimeRange;
}

// Presets stop at 72 hours: past that a search, its histogram and facets
// read days of files, so a longer range is a deliberate Custom Range.
const lastPreset = (value: string, label: string, amount: number, unit: 'minutes' | 'hours') => ({
  label,
  value,
  // Start on a whole minute so the histogram's per-minute counts cover the
  // oldest edge without a raw read of a cold file.
  getRange: (): TimeRange => ({
    from: dayjs().subtract(amount, unit).startOf('minute'),
    to: dayjs(),
  }),
});

const presets: TimeRangePreset[] = [
  lastPreset('5m', 'Last 5 minutes', 5, 'minutes'),
  lastPreset('15m', 'Last 15 minutes', 15, 'minutes'),
  lastPreset('30m', 'Last 30 minutes', 30, 'minutes'),
  lastPreset('1h', 'Last 1 hour', 1, 'hours'),
  lastPreset('3h', 'Last 3 hours', 3, 'hours'),
  lastPreset('6h', 'Last 6 hours', 6, 'hours'),
  lastPreset('12h', 'Last 12 hours', 12, 'hours'),
  lastPreset('24h', 'Last 24 hours', 24, 'hours'),
  lastPreset('36h', 'Last 36 hours', 36, 'hours'),
  lastPreset('48h', 'Last 48 hours', 48, 'hours'),
  lastPreset('72h', 'Last 72 hours', 72, 'hours'),
];

/** The preset a picker starts on unless told otherwise. */
export const DEFAULT_TIME_PRESET = '15m';

/** The picker's own selection - a preset, or "custom" plus its range. */
export interface TimeRangeSelection {
  preset: string;
  customRange: [dayjs.Dayjs, dayjs.Dayjs] | null;
}

export interface TimeRangeHelpers {
  isUntilNow: () => boolean;
  refreshRange: () => void;
  /** Switch to "Custom Range" set to `range` (e.g. a histogram bar zoom). */
  applyCustomRange: (range: TimeRange) => void;
  /** Snapshot / restore the selection - how a zoom is undone. */
  getSelection: () => TimeRangeSelection;
  restoreSelection: (selection: TimeRangeSelection) => void;
}

interface Props {
  onChange: (range: TimeRange | null) => void;
  onGetCurrentRange?: (getCurrentRange: () => TimeRange | null) => void;
  onGetRangeHelpers?: (helpers: TimeRangeHelpers) => void;
  // Compact toolbar layout: no labels, smaller controls.
  compact?: boolean;
  /** Preset value to start on, e.g. '24h'. */
  defaultPreset?: string;
}

export const TimeRangePickerComponent = ({
  onChange,
  onGetCurrentRange,
  onGetRangeHelpers,
  compact = false,
  defaultPreset = DEFAULT_TIME_PRESET,
}: Props): React.JSX.Element => {
  // A short default window, so landing on a page doesn't query a day of data.
  const [selectedPreset, setSelectedPreset] = useState<string>(defaultPreset);
  const [customRange, setCustomRange] = useState<[dayjs.Dayjs, dayjs.Dayjs] | null>(null);

  const timeFormat = useMemo(() => getUserShortTimeFormat(), []);

  const getCurrentRange = (): TimeRange | null => {
    if (selectedPreset === 'custom') {
      return customRange ? { from: customRange[0], to: customRange[1] } : null;
    }

    const preset = presets.find((p) => p.value === selectedPreset);
    return preset ? preset.getRange() : null;
  };

  const isUntilNow = (): boolean => {
    return selectedPreset !== 'custom';
  };

  const refreshRange = (): void => {
    if (selectedPreset !== 'custom') {
      // Recalculate so "now" moves forward.
      const preset = presets.find((p) => p.value === selectedPreset);
      if (preset) {
        const range = preset.getRange();
        onChange(range);
      }
    }
  };

  const handlePresetChange = (presetValue: string) => {
    setSelectedPreset(presetValue);

    if (presetValue === 'custom') {
      const range = customRange ? { from: customRange[0], to: customRange[1] } : null;
      onChange(range);
    } else {
      const preset = presets.find((p) => p.value === presetValue);
      if (preset) {
        const range = preset.getRange();
        onChange(range);
      }
    }
  };

  const handleCustomRangeChange = (dates: [dayjs.Dayjs, dayjs.Dayjs] | null) => {
    setCustomRange(dates);

    if (selectedPreset === 'custom') {
      const range = dates ? { from: dates[0], to: dates[1] } : null;
      onChange(range);
    }
  };

  useEffect(() => {
    const defaultPreset = presets.find((p) => p.value === selectedPreset);
    if (defaultPreset) {
      const range = defaultPreset.getRange();
      onChange(range);
    }
  }, []);

  useEffect(() => {
    if (onGetCurrentRange) {
      onGetCurrentRange(getCurrentRange);
    }
  }, [selectedPreset, customRange, onGetCurrentRange]);

  const applyCustomRange = (range: TimeRange): void => {
    setSelectedPreset('custom');
    setCustomRange([range.from, range.to]);
    onChange(range);
  };

  const getSelection = (): TimeRangeSelection => ({ preset: selectedPreset, customRange });

  const restoreSelection = (selection: TimeRangeSelection): void => {
    setSelectedPreset(selection.preset);
    setCustomRange(selection.customRange);
    if (selection.preset === 'custom') {
      onChange(
        selection.customRange
          ? { from: selection.customRange[0], to: selection.customRange[1] }
          : null,
      );
    } else {
      const preset = presets.find((p) => p.value === selection.preset);
      if (preset) onChange(preset.getRange());
    }
  };

  useEffect(() => {
    if (onGetRangeHelpers) {
      onGetRangeHelpers({
        isUntilNow,
        refreshRange,
        applyCustomRange,
        getSelection,
        restoreSelection,
      });
    }
  }, [selectedPreset, customRange, onGetRangeHelpers]);

  const rangePicker = (
    <RangePicker
      size={compact ? 'small' : 'middle'}
      showTime={{
        // Seconds shown: a histogram zoom can pick a range under a minute.
        format: timeFormat.use12Hours ? 'h:mm:ss A' : 'HH:mm:ss',
        use12Hours: timeFormat.use12Hours,
      }}
      value={customRange}
      onChange={(dates) => {
        const validDates =
          dates && dates[0] && dates[1]
            ? ([dates[0], dates[1]] as [dayjs.Dayjs, dayjs.Dayjs])
            : null;
        handleCustomRangeChange(validDates);
      }}
      placeholder={['Start time', 'End time']}
      // Fits two "2026-11-02 10:33:31 PM" inputs; narrower cut off AM/PM.
      className={`max-w-full ${compact ? 'w-96' : 'w-[26rem]'}`}
    />
  );

  const selectPicker = (
    <Select
      size={compact ? 'small' : 'middle'}
      value={selectedPreset}
      onChange={handlePresetChange}
      className={compact ? 'w-36' : 'w-48'}
      suffixIcon={<ClockCircleOutlined />}
    >
      <Select.Option value="custom">Custom Range</Select.Option>

      {presets.map((preset) => (
        <Select.Option key={preset.value} value={preset.value}>
          {preset.label}
        </Select.Option>
      ))}
    </Select>
  );

  if (compact) {
    return (
      <div className="flex items-center gap-1.5">
        {selectPicker}
        {selectedPreset === 'custom' && rangePicker}
      </div>
    );
  }

  return (
    <div className="space-y-3">
      <div>
        <label className="mb-1 block text-sm font-medium text-gray-700 dark:text-gray-200">
          Time Range
        </label>
        {selectPicker}
      </div>

      {selectedPreset === 'custom' && (
        <div>
          <label className="mb-1 block text-sm font-medium text-gray-700 dark:text-gray-200">
            Select Custom Time Range
          </label>
          {rangePicker}
        </div>
      )}
    </div>
  );
};
