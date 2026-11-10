import { DownOutlined } from '@ant-design/icons';
import { Button, Checkbox, Input, Popover } from 'antd';
import { useMemo, useState } from 'react';

import type { CostDimension, FacetValue } from '../../../entity/cost';
import { dimensionLabel, valueLabel } from '../lib/explorer';
import { formatMoneyCompact } from '../lib/format';

interface Props {
  dimension: CostDimension;
  /** Values with the other filters applied, so only ones in scope are offered. */
  values: FacetValue[];
  selected: string[];
  /** The other filters, named under the list ("For Provider: Azure"). */
  scope: string;
  onChange: (values: string[]) => void;
}

const SHOWN = 200;

export const FacetFilter = ({ dimension, values, selected, scope, onChange }: Props) => {
  const [search, setSearch] = useState('');
  const label = dimensionLabel(dimension);

  // A selected value outside the scope stays listed so it can be cleared.
  const options = useMemo(() => {
    const known = new Set(values.map((value) => value.value));
    const all: FacetValue[] = [
      ...values,
      ...selected.filter((value) => !known.has(value)).map((value) => ({ value, amounts: {} })),
    ];
    const needle = search.trim().toLowerCase();
    return needle
      ? all.filter((option) =>
          valueLabel(dimension, option.value, option.label).toLowerCase().includes(needle),
        )
      : all;
  }, [values, selected, search, dimension]);

  const toggle = (value: string, on: boolean) =>
    onChange(on ? [...selected, value] : selected.filter((item) => item !== value));

  const content = (
    <div className="flex w-80 max-w-[80vw] flex-col gap-2">
      <Input
        size="small"
        allowClear
        placeholder={`Search ${label.toLowerCase()}`}
        value={search}
        onChange={(event) => setSearch(event.target.value)}
      />
      <div className="max-h-72 overflow-y-auto">
        {options.length === 0 && (
          <div className="py-4 text-center text-xs text-gray-500 dark:text-gray-400">No values</div>
        )}
        {options.slice(0, SHOWN).map((option) => (
          <label
            key={option.value}
            className="flex cursor-pointer items-center gap-2 rounded px-1 py-1 text-sm hover:bg-gray-50 dark:hover:bg-gray-700"
          >
            <Checkbox
              checked={selected.includes(option.value)}
              onChange={(event) => toggle(option.value, event.target.checked)}
            />
            <span className="min-w-0 flex-1 truncate text-gray-800 dark:text-gray-200">
              {valueLabel(dimension, option.value, option.label)}
            </span>
            <span className="shrink-0 text-xs text-gray-500 tabular-nums dark:text-gray-400">
              {Object.entries(option.amounts)
                .map(([currency, amount]) => formatMoneyCompact(amount, currency))
                .join(' · ')}
            </span>
          </label>
        ))}
        {options.length > SHOWN && (
          <div className="px-1 py-1 text-xs text-gray-500 dark:text-gray-400">
            {options.length - SHOWN} more; search to narrow
          </div>
        )}
      </div>
      <div className="flex items-center justify-between gap-2 border-t border-gray-200 pt-2 text-xs text-gray-500 dark:border-gray-700 dark:text-gray-400">
        <span className="min-w-0 truncate" title={scope}>
          {scope ? `For ${scope}` : 'All values in the range'}
        </span>
        {selected.length > 0 && (
          <Button size="small" type="link" onClick={() => onChange([])}>
            Clear
          </Button>
        )}
      </div>
    </div>
  );

  return (
    <Popover trigger="click" placement="bottomLeft" content={content}>
      <Button
        size="small"
        type={selected.length ? 'primary' : 'default'}
        ghost={selected.length > 0}
      >
        {label}
        {selected.length > 0 &&
          `: ${selected.length === 1 ? valueLabel(dimension, selected[0]) : selected.length}`}
        <DownOutlined className="text-[10px]" />
      </Button>
    </Popover>
  );
};
