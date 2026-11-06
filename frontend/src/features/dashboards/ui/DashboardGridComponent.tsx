import { DeleteOutlined, EditOutlined, HolderOutlined } from '@ant-design/icons';
import { Button, Popconfirm } from 'antd';
import React, { useMemo } from 'react';
// The /legacy entry keeps react-grid-layout v1's runtime API; only the types
// changed (`Layout` is now the array, `LayoutItem` one item).
import RGL, { type Layout, WidthProvider } from 'react-grid-layout/legacy';

import 'react-grid-layout/css/styles.css';
import 'react-resizable/css/styles.css';

import type { Panel } from '../../../entity/dashboards';
import type { TimeRange } from '../../query';
import { type DashboardFilters, applyFiltersToPanel } from '../lib/dashboardFilters';
import { applyVariablesToPanel } from '../lib/dashboardVariables';
import { PanelChartComponent } from './PanelChartComponent';

const GridLayout = WidthProvider(RGL);

const GRID_COLUMNS = 12;
const ROW_HEIGHT_PX = 28;

// Memoized so the filtered panel keeps its identity across grid re-renders;
// otherwise every drag or state change refetched every panel.
const DashboardPanelChart = ({
  projectId,
  panel,
  filters,
  variableValues,
  timeRange,
  refreshToken,
}: {
  projectId: string;
  panel: Panel;
  filters?: DashboardFilters;
  variableValues?: Record<string, string>;
  timeRange: TimeRange;
  refreshToken?: number;
}): React.JSX.Element => {
  const derivedPanel = useMemo(
    () =>
      applyFiltersToPanel(
        variableValues ? applyVariablesToPanel(panel, variableValues) : panel,
        filters ?? {},
      ),
    [panel, filters, variableValues],
  );

  return (
    <PanelChartComponent
      projectId={projectId}
      panel={derivedPanel}
      timeRange={timeRange}
      refreshToken={refreshToken}
    />
  );
};

interface Props {
  projectId: string;
  panels: Panel[];
  timeRange: TimeRange;
  isEditing: boolean;
  refreshToken?: number;
  filters?: DashboardFilters;
  variableValues?: Record<string, string>;
  onLayoutChange?: (panels: Panel[]) => void;
  onEditPanel?: (panel: Panel) => void;
  onDeletePanel?: (panelId: string) => void;
}

export const DashboardGridComponent = ({
  projectId,
  panels,
  timeRange,
  isEditing,
  refreshToken,
  filters,
  variableValues,
  onLayoutChange,
  onEditPanel,
  onDeletePanel,
}: Props): React.JSX.Element => {
  const layout: Layout = panels.map((panel) => ({
    i: panel.id,
    x: panel.gridPos.x,
    y: panel.gridPos.y,
    w: panel.gridPos.w,
    h: panel.gridPos.h,
    minW: 2,
    minH: 3,
  }));

  const handleLayoutChange = (newLayout: Layout) => {
    if (!isEditing || !onLayoutChange) return;

    const updated = panels.map((panel) => {
      const item = newLayout.find((l) => l.i === panel.id);
      if (!item) return panel;
      return { ...panel, gridPos: { x: item.x, y: item.y, w: item.w, h: item.h } };
    });
    onLayoutChange(updated);
  };

  if (panels.length === 0) {
    return (
      <div className="flex h-64 items-center justify-center rounded border border-dashed border-gray-300 text-gray-400 dark:border-gray-600 dark:text-gray-400">
        No panels yet - add one to get started
      </div>
    );
  }

  return (
    <GridLayout
      layout={layout}
      cols={GRID_COLUMNS}
      rowHeight={ROW_HEIGHT_PX}
      isDraggable={isEditing}
      isResizable={isEditing}
      onLayoutChange={handleLayoutChange}
      draggableHandle=".panel-drag-handle"
      compactType="vertical"
    >
      {panels.map((panel) => (
        <div
          key={panel.id}
          className="overflow-hidden rounded border border-gray-200 bg-white shadow-sm dark:border-gray-700 dark:bg-gray-800"
        >
          <div className="flex items-center justify-between border-b border-gray-100 px-2 py-1.5">
            <div className="flex min-w-0 items-center gap-1.5">
              {isEditing && (
                <HolderOutlined className="panel-drag-handle cursor-move text-gray-400 dark:text-gray-400" />
              )}
              <span className="truncate text-sm font-medium text-gray-700 dark:text-gray-200">
                {panel.title || 'Untitled panel'}
              </span>
            </div>

            {isEditing && (
              <div className="flex flex-shrink-0 gap-1">
                <Button
                  size="small"
                  type="text"
                  icon={<EditOutlined />}
                  onClick={() => onEditPanel?.(panel)}
                />
                <Popconfirm title="Delete this panel?" onConfirm={() => onDeletePanel?.(panel.id)}>
                  <Button size="small" type="text" danger icon={<DeleteOutlined />} />
                </Popconfirm>
              </div>
            )}
          </div>

          <div style={{ height: 'calc(100% - 33px)' }} className="p-2">
            <DashboardPanelChart
              projectId={projectId}
              panel={panel}
              filters={filters}
              variableValues={variableValues}
              timeRange={timeRange}
              refreshToken={refreshToken}
            />
          </div>
        </div>
      ))}
    </GridLayout>
  );
};
