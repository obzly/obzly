import { Gauge } from '../../../shared/ui/v2';
import type { GaugeModel } from '../lib/capacity';
import type { Loaded } from '../lib/useLoad';
import { PanelBody, Pill } from './parts';

export interface CapacityItem {
  key: string;
  title: string;
  subtitle: string;
  color: string;
  state: Loaded<GaugeModel>;
  onOpen?: () => void;
}

/** The budgets and capacity row: one gauge card per item, each loading on its own. */
export const CapacityPanels = ({ items }: { items: CapacityItem[] }) => (
  <section
    aria-label="Budgets and capacity"
    className="grid grid-cols-[repeat(auto-fit,minmax(220px,1fr))] gap-4"
  >
    {items.map((item) => {
      const body = (
        <>
          <span className="flex items-start justify-between gap-2">
            <span className="text-[15px] font-semibold text-gray-900 dark:text-gray-50">
              {item.title}
            </span>
            {item.state.data && (
              <Pill tone={item.state.data.pill.tone}>{item.state.data.pill.text}</Pill>
            )}
          </span>
          <span className="text-xs text-gray-500 dark:text-gray-400">{item.subtitle}</span>
          <PanelBody state={item.state} minHeight={180}>
            {(gauge) => (
              <span className="flex flex-col items-center gap-2 pt-1">
                <Gauge
                  ratio={gauge.ratio}
                  value={gauge.value}
                  caption={gauge.caption}
                  color={item.color}
                  size={140}
                  label={`${item.title}: ${gauge.value} ${gauge.caption}`}
                />
                <span className="text-center text-xs text-gray-500 dark:text-gray-400">
                  {gauge.foot}
                </span>
              </span>
            )}
          </PanelBody>
        </>
      );
      const cls =
        'flex min-w-0 flex-col gap-1 rounded-2xl border border-gray-200 bg-white p-5 text-left dark:border-gray-700 dark:bg-gray-800';
      return item.onOpen ? (
        <button
          key={item.key}
          type="button"
          onClick={item.onOpen}
          className={`${cls} hover:border-accent-500/60 cursor-pointer`}
        >
          {body}
        </button>
      ) : (
        <div key={item.key} className={cls}>
          {body}
        </div>
      );
    })}
  </section>
);
