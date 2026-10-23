/* Reuse full navigation coverage with a 133-chart worker fixture and real ECharts Canvas. */
process.env.EXCEL_CHART_FIXTURE = '1';
require('./full-navigation.cjs');
