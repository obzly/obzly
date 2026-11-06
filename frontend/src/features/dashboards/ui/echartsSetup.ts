// Registers only the ECharts pieces in use to keep the full bundle out; import
// `echarts` from here, not from the package.
import { BarChart, GraphChart, HeatmapChart, LineChart, PieChart } from 'echarts/charts';
import {
  GridComponent,
  LegendComponent,
  MarkLineComponent,
  TooltipComponent,
  VisualMapComponent,
} from 'echarts/components';
import * as echarts from 'echarts/core';
import { CanvasRenderer } from 'echarts/renderers';

echarts.use([
  BarChart,
  LineChart,
  PieChart,
  // Synthetics' availability heatmap (location x hour).
  HeatmapChart,
  VisualMapComponent,
  // Synthetics' dependency map.
  GraphChart,
  GridComponent,
  LegendComponent,
  // The logs page histogram's cursor line (LogHistogramComponent).
  MarkLineComponent,
  TooltipComponent,
  CanvasRenderer,
]);

export default echarts;
