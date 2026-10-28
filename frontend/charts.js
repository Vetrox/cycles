// Only the ECharts parts this app uses; the bundler drops everything else.
import * as echarts from 'echarts/core';
import { LineChart, BoxplotChart, ScatterChart } from 'echarts/charts';
import { GridComponent, TooltipComponent, LegendComponent, TitleComponent, DataZoomInsideComponent } from 'echarts/components';
import { CanvasRenderer } from 'echarts/renderers';

echarts.use([LineChart, BoxplotChart, ScatterChart, GridComponent, TooltipComponent, LegendComponent,
  TitleComponent, DataZoomInsideComponent, CanvasRenderer]);
export default echarts;
