import { Button, Checkbox, Radio, Select } from 'antd';
import React, { useMemo } from 'react';

import { LOCAL_TIMEZONE, getAvailableTimezones, getTimezoneLabel } from '../../../shared/time';
import type {
  TraceDetailPlacement,
  TraceDetailWidth,
  TraceRowDensity,
  TraceTextSize,
  TraceViewSettings,
} from '../lib/traceViewSettings';

interface Props {
  settings: TraceViewSettings;
  /** Any change here makes the layout "custom" (no preset highlighted). */
  onChange: (patch: Partial<TraceViewSettings>) => void;
  onReset: () => void;
  timezone: string;
  onTimezoneChange: (timezone: string) => void;
}

const SectionTitle = ({ children }: { children: React.ReactNode }) => (
  <div className="col-span-2 border-t border-gray-200 pt-2 text-[11px] font-semibold tracking-wider text-gray-500 uppercase first:border-t-0 first:pt-0 dark:border-gray-700 dark:text-gray-400">
    {children}
  </div>
);

const Row = ({ label, children }: { label: string; children: React.ReactNode }) => (
  <>
    <span className="text-gray-500 dark:text-gray-400">{label}</span>
    <div role="group" aria-label={label} className="min-w-0">
      {children}
    </div>
  </>
);

/** The gear popover: display timezone and the results layout. */
export const TraceViewSettingsComponent = ({
  settings,
  onChange,
  onReset,
  timezone,
  onTimezoneChange,
}: Props): React.JSX.Element => {
  const timezoneOptions = useMemo(
    () =>
      [LOCAL_TIMEZONE, ...getAvailableTimezones()].map((tz) => ({
        value: tz,
        label: getTimezoneLabel(tz),
      })),
    [],
  );

  return (
    <div className="grid w-[340px] max-w-[80vw] grid-cols-[104px_minmax(0,1fr)] items-center gap-x-2.5 gap-y-2 text-[13px]">
      <SectionTitle>Display</SectionTitle>
      <Row label="Timezone">
        <Select
          size="small"
          showSearch
          value={timezone}
          onChange={onTimezoneChange}
          options={timezoneOptions}
          className="w-full"
          popupMatchSelectWidth={false}
          aria-label="Timezone"
          filterOption={(input, option) =>
            (option?.label ?? '').toLowerCase().includes(input.toLowerCase().replace(/ /g, '_')) ||
            (option?.label ?? '').toLowerCase().includes(input.toLowerCase())
          }
        />
      </Row>

      <SectionTitle>Layout</SectionTitle>
      <Row label="Panels">
        <Checkbox
          checked={settings.showSummary}
          onChange={(e) => onChange({ showSummary: e.target.checked })}
        >
          Summary
        </Checkbox>
      </Row>
      <Row label="Details">
        <Radio.Group
          size="small"
          optionType="button"
          buttonStyle="solid"
          value={settings.detail}
          onChange={(e) => onChange({ detail: e.target.value as TraceDetailPlacement })}
          options={[
            { value: 'side', label: 'Side' },
            { value: 'below', label: 'Below' },
            { value: 'inline', label: 'In row' },
          ]}
        />
      </Row>
      <Row label="Details width">
        <Radio.Group
          size="small"
          optionType="button"
          buttonStyle="solid"
          value={settings.detail === 'side' ? settings.detailWidth : undefined}
          // A width only applies beside the list.
          onChange={(e) =>
            onChange({ detailWidth: e.target.value as TraceDetailWidth, detail: 'side' })
          }
          options={[
            { value: 's', label: 'S' },
            { value: 'm', label: 'M' },
            { value: 'l', label: 'L' },
          ]}
        />
      </Row>
      <Row label="Rows">
        <Radio.Group
          size="small"
          optionType="button"
          buttonStyle="solid"
          value={settings.density}
          onChange={(e) => onChange({ density: e.target.value as TraceRowDensity })}
          options={[
            { value: 'comfortable', label: 'Comfortable' },
            { value: 'compact', label: 'Compact' },
          ]}
        />
      </Row>
      <Row label="Text size">
        <Radio.Group
          size="small"
          optionType="button"
          buttonStyle="solid"
          value={settings.textSize}
          onChange={(e) => onChange({ textSize: e.target.value as TraceTextSize })}
          options={[
            { value: 12, label: '12' },
            { value: 13, label: '13' },
            { value: 14, label: '14' },
          ]}
        />
      </Row>

      <div className="col-span-2 flex items-center justify-between gap-2 border-t border-gray-200 pt-2 dark:border-gray-700">
        <span className="text-[11.5px] text-gray-500 dark:text-gray-400">
          Saved for you in this browser
        </span>
        <Button type="link" size="small" onClick={onReset} className="px-0">
          Reset
        </Button>
      </div>
    </div>
  );
};
