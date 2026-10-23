// Only loaded when the user opens a source chart, not for ordinary table previews.
import { init, use } from 'echarts/core';
import { LineChart, BarChart } from 'echarts/charts';
import { GridComponent, LegendComponent, TooltipComponent, TitleComponent } from 'echarts/components';
import { CanvasRenderer } from 'echarts/renderers';

use([LineChart, BarChart, GridComponent, LegendComponent, TooltipComponent, TitleComponent, CanvasRenderer]);
export { init };
