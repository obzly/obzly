import { CheckOutlined, DownOutlined } from '@ant-design/icons';
import { Button, Popover } from 'antd';
import type { JSX } from 'react';
import { useState } from 'react';

import type { WebAnalyticsSource } from '../../../entity/webAnalytics';
import { formatShare } from '../lib/format';
import { sourceGaps, sourceLabel } from '../lib/sources';

interface Props {
  sources: WebAnalyticsSource[];
  /** Keys in the report; empty means every source. */
  selected: string[];
  onApply: (keys: string[]) => void;
}

/** Multi-select of the access-log sources the report is built from. */
export const SourcePicker = ({ sources, selected, onApply }: Props): JSX.Element => {
  const [open, setOpen] = useState(false);
  const [draft, setDraft] = useState<string[]>([]);
  const current = selected.length === 0 ? sources.map((s) => s.key) : selected;

  const toggle = (key: string) =>
    setDraft((d) => (d.includes(key) ? d.filter((k) => k !== key) : [...d, key]));

  const content = (
    <div
      role="dialog"
      aria-label="Data sources for this report"
      className="flex w-[440px] max-w-[86vw] flex-col gap-2"
    >
      <span className="flex items-center justify-between">
        <strong className="font-semibold text-gray-900 dark:text-gray-50">
          Build the report from
        </strong>
        <span className="flex gap-2.5">
          <Button
            type="link"
            size="small"
            className="!px-0"
            onClick={() => setDraft(sources.map((s) => s.key))}
          >
            All
          </Button>
          <Button type="link" size="small" className="!px-0" onClick={() => setDraft([])}>
            None
          </Button>
        </span>
      </span>
      <div className="flex max-h-[420px] flex-col gap-2 overflow-y-auto">
        {sources.map((source) => {
          const on = draft.includes(source.key);
          const gaps = sourceGaps(source);
          return (
            <button
              key={source.key}
              type="button"
              role="checkbox"
              aria-checked={on}
              onClick={() => toggle(source.key)}
              className="flex cursor-pointer items-start gap-2.5 rounded-[10px] border border-gray-200 bg-transparent p-2 text-left dark:border-gray-700"
            >
              <span
                className={`mt-0.5 flex h-4 w-4 flex-none items-center justify-center rounded border-[1.5px] text-[11px] text-white ${
                  on
                    ? 'border-accent-600 bg-accent-600'
                    : 'border-gray-400 bg-transparent dark:border-gray-500'
                }`}
              >
                {on && <CheckOutlined />}
              </span>
              <span className="flex min-w-0 flex-1 flex-col gap-0.5 text-gray-800 dark:text-gray-100">
                <span className="flex justify-between gap-2">
                  <strong className="font-mono text-[13px] font-semibold">{source.name}</strong>
                  <span className="text-xs text-gray-500 dark:text-gray-400">
                    {source.requests > 0
                      ? `${formatShare(source.share)} of requests`
                      : 'no requests in this range'}
                  </span>
                </span>
                <span className="text-xs">
                  {[source.kind, source.format, source.where].filter(Boolean).join(' · ')}
                </span>
                {source.hosts.length > 0 && (
                  <span className="truncate font-mono text-xs text-gray-500 dark:text-gray-400">
                    {source.hosts.join(', ')}
                  </span>
                )}
                <span className="flex min-w-0 items-baseline gap-1.5 text-xs">
                  <span className="flex-none text-gray-500 dark:text-gray-400">Identified by</span>
                  <span className="font-mono text-[11.5px] break-all">
                    {source.identity.map((a) => `${a.name}=${a.value}`).join(' · ') ||
                      'unidentified'}
                  </span>
                </span>
                {source.mergedFrom.length > 0 && (
                  <span className="text-xs text-gray-500 dark:text-gray-400">
                    Includes {source.mergedFrom.length} merged source
                    {source.mergedFrom.length === 1 ? '' : 's'}
                  </span>
                )}
                <span
                  className={`text-xs ${gaps.length ? 'text-orange-600 dark:text-orange-400' : 'text-emerald-600 dark:text-emerald-400'}`}
                >
                  {gaps.length ? gaps.join(' · ') : 'all fields'}
                </span>
              </span>
            </button>
          );
        })}
      </div>
      {draft.length === 0 && (
        <span className="text-xs text-red-600 dark:text-red-400">Pick at least one source.</span>
      )}
      <span className="flex items-center justify-between gap-2 pt-1">
        <span className="text-xs text-gray-500 dark:text-gray-400">
          Found automatically: logs with access-log fields (status, method, path), grouped by{' '}
          <span className="font-mono">obzly_source</span> if the collector sets it, else Kubernetes
          workload, else service and host. Rename or merge in settings.
        </span>
        <span className="flex flex-none gap-1.5">
          <Button size="small" onClick={() => setOpen(false)}>
            Cancel
          </Button>
          <Button
            size="small"
            type="primary"
            disabled={draft.length === 0}
            onClick={() => {
              onApply(draft.length === sources.length ? [] : draft);
              setOpen(false);
            }}
          >
            Build report
          </Button>
        </span>
      </span>
    </div>
  );

  return (
    <Popover
      open={open}
      trigger="click"
      placement="bottomRight"
      content={content}
      onOpenChange={(next) => {
        if (next) setDraft(current);
        setOpen(next);
      }}
    >
      <Button
        aria-haspopup="dialog"
        aria-expanded={open}
        className="min-w-[190px] !justify-between"
      >
        <span className="text-xs text-gray-500 dark:text-gray-400">Data sources</span>
        <strong className="font-semibold">{sourceLabel(sources, selected)}</strong>
        <DownOutlined className="text-gray-400" />
      </Button>
    </Popover>
  );
};
