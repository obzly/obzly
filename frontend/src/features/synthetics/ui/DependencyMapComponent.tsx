import type { EChartsOption } from 'echarts';
import { memo, useCallback, useMemo } from 'react';

import type { CheckView } from '../../../entity/synthetics';
import { escapeHtml } from '../../../shared/lib';
import { dependencyLayout } from '../lib/dependencyLayout';
import { type MonitorStatus, STATUS_COLOR, STATUS_LABEL } from '../lib/status';
import type { MonitorData } from '../lib/useMonitorData';
import { type ChartColors, EChart } from './EChart';
import { Panel } from './parts';

interface Props {
  checks: CheckView[];
  data: Map<string, MonitorData>;
  onOpen: (check: CheckView) => void;
}

const DependencyMap = ({ checks, data, onOpen }: Props) => {
  const layout = useMemo(() => dependencyLayout(checks), [checks]);
  const statusOf = useCallback(
    (check: CheckView): MonitorStatus => data.get(check.id)?.state.status ?? 'none',
    [data],
  );

  const option = useCallback(
    (colors: ChartColors): EChartsOption => ({
      animation: false,
      tooltip: {
        backgroundColor: colors.tooltip,
        borderColor: colors.axis,
        textStyle: { color: colors.text, fontSize: 12 },
        formatter: (params) => {
          const item = params as { dataType?: string; dataIndex: number };
          if (item.dataType !== 'node') return '';
          const node = layout.nodes[item.dataIndex];
          const impacted = data.get(node.check.id)?.impactedBy;
          return [
            escapeHtml(node.check.name),
            STATUS_LABEL[statusOf(node.check)],
            node.check.folder ? `Folder: ${escapeHtml(node.check.folder)}` : '',
            impacted
              ? `Parent down: ${impacted.map((parent) => escapeHtml(parent.name)).join(', ')}`
              : '',
          ]
            .filter(Boolean)
            .join('<br>');
        },
      },
      series: [
        {
          type: 'graph',
          layout: 'none',
          roam: true,
          edgeSymbol: ['none', 'arrow'],
          edgeSymbolSize: 9,
          label: { show: true, position: 'bottom', color: colors.text, fontSize: 12 },
          lineStyle: { color: colors.text, opacity: 0.35, width: 1.6 },
          data: layout.nodes.map((node) => {
            const status = statusOf(node.check);
            return {
              id: node.check.id,
              name: node.check.name,
              x: node.x,
              y: node.y,
              symbolSize: 26,
              itemStyle: {
                // Impacted: a ring with a faded fill, as the half-filled bullet elsewhere
                // (an alpha colour, since opacity would fade its label too).
                color: status === 'impacted' ? `${STATUS_COLOR.impacted}66` : STATUS_COLOR[status],
                borderColor: status === 'impacted' ? STATUS_COLOR.impacted : 'transparent',
                borderWidth: 3,
              },
            };
          }),
          links: layout.links,
        },
      ],
    }),
    [layout, data, statusOf],
  );

  const unrelated = checks.length - layout.nodes.length;
  return (
    <Panel
      title="Dependency map"
      subtitle="Parents above their children. While a parent is down its children are Impacted and their alerts are muted, so one outage sends one alert. Click a monitor to open it; drag or scroll to move and zoom."
    >
      {layout.nodes.length === 0 ? (
        <div className="py-12 text-center text-sm text-gray-500 dark:text-gray-400">
          No dependencies yet. Set them in a monitor&apos;s editor (Depends on).
        </div>
      ) : (
        <EChart
          option={option}
          height={Math.min(640, Math.max(280, layout.height + 120))}
          onClick={(params) => {
            const node = layout.nodes[params.dataIndex];
            if ((params as { dataType?: string }).dataType === 'node' && node) onOpen(node.check);
          }}
        />
      )}
      <p className="mt-2 mb-0 text-xs text-gray-500 dark:text-gray-400">
        {unrelated > 0 &&
          `${unrelated} monitor${unrelated > 1 ? 's' : ''} without dependencies not shown. `}
        Muting needs parent and child in the same destination project; across projects the child is
        still shown as Impacted.
      </p>
    </Panel>
  );
};

export const DependencyMapComponent = memo(DependencyMap);
