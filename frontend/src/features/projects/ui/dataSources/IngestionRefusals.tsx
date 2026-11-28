import { type JSX } from 'react';

import type { IngestionRefusal } from '../../../../entity/projects';
import { Pill } from '../ProjectPageShell';
import { droppedCount, formatAgo, recordNoun, refusalLabel } from './ingestionStatusFormat';

interface Props {
  refused: IngestionRefusal[];
  windowHours: number;
  now: number;
}

/**
 * What the project's receivers refused recently, by signal and reason. A
 * retried refusal (429/503) only delays the data; anything else is dropped by
 * the sender, so it is called out as lost.
 */
export const IngestionRefusals = ({ refused, windowHours, now }: Props): JSX.Element | null => {
  if (refused.length === 0) return null;
  const dropped = droppedCount(refused);

  return (
    <section
      aria-label="Refused data"
      className={`mt-3 flex flex-col gap-2 rounded-xl px-4 py-3 ${
        dropped > 0
          ? 'bg-red-500/10 text-gray-800 dark:text-gray-100'
          : 'bg-orange-500/10 text-gray-800 dark:text-gray-100'
      }`}
    >
      <p className="m-0 text-sm font-medium">
        {dropped > 0
          ? `${dropped.toLocaleString()} records were refused and dropped by their senders in the last ${windowHours} h.`
          : `Some requests were refused in the last ${windowHours} h and their senders were told to retry them.`}
      </p>
      <ul className="m-0 flex list-none flex-col gap-1.5 p-0">
        {refused.map((r) => (
          <li
            key={`${r.signal}/${r.reason}`}
            className="flex flex-wrap items-center gap-x-3 gap-y-1 text-[13px]"
          >
            <span className="min-w-0 flex-1 basis-56">{refusalLabel(r.reason)}</span>
            <span className="text-gray-900 tabular-nums dark:text-gray-50">
              {r.count.toLocaleString()} {recordNoun(r.signal)}
            </span>
            <Pill tone={r.retried ? 'warn' : 'bad'}>
              {r.retried ? 'sender retries' : 'dropped by sender'}
            </Pill>
            <span className="text-xs text-gray-500 dark:text-gray-400">
              last {formatAgo(r.lastAt, now)}
            </span>
          </li>
        ))}
      </ul>
      <p className="m-0 text-xs text-gray-500 dark:text-gray-400">
        Counts are per attempt: a request refused and sent again counts each time.
      </p>
    </section>
  );
};
