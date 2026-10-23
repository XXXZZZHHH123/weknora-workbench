// Only loaded when the user opens a source chart, not for ordinary table previews.
import { init, use } from 'echarts/core';
// ECharts 6 publishes these public, side-effectful entries. Register exactly
// the same installers without making Rollup parse every charts/components barrel.
import 'echarts/lib/chart/line';
import 'echarts/lib/chart/bar';
import 'echarts/lib/component/grid';
import 'echarts/lib/component/legend';
import 'echarts/lib/component/tooltip';
import 'echarts/lib/component/title';
import { CanvasRenderer } from 'echarts/renderers';

use([CanvasRenderer]);
export { init };
