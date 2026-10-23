// ECharts 6.0.0 explicitly exports these official JS entries but does not
// publish adjacent declarations. They only register their chart/component;
// application values and types continue to come from the typed core API.
declare module 'echarts/lib/chart/line' {}
declare module 'echarts/lib/chart/bar' {}
declare module 'echarts/lib/component/grid' {}
declare module 'echarts/lib/component/legend' {}
declare module 'echarts/lib/component/tooltip' {}
declare module 'echarts/lib/component/title' {}
