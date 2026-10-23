import type { EChartsCoreOption } from 'echarts/core';
import type { ExcelPreviewChart } from './excelPreviewCharts';

export function excelSourceChartOption(chart: ExcelPreviewChart, style: { color: string; muted: string; border: string; font: string }) {
  const notices: string[] = [];
  const valueAxes = chart.axes.filter(axis => axis.type === 'value');
  const categoryAxes = chart.axes.filter(axis => axis.type !== 'value');
  const ys: ExcelPreviewChart['axes'] = valueAxes.length ? valueAxes : [{ id: '', type: 'value' }];
  const xs: ExcelPreviewChart['axes'] = categoryAxes.length ? categoryAxes : [{ id: '', type: 'category' }];
  const supported = chart.series.filter(series => {
    if (series.axisId && !ys.some(axis => axis.id === series.axisId) || series.categoryAxisId && !xs.some(axis => axis.id === series.categoryAxisId)) {
      notices.push('部分系列的原件坐标轴关系缺失，未猜测其坐标轴。'); return false;
    }
    if (xs.find(axis => axis.id === series.categoryAxisId)?.type === 'date'
        && !series.categoryTimes?.some(value => typeof value === 'number' && Number.isFinite(value))) {
      notices.push('部分日期轴缺少可靠时间缓存，未改画为等距分类轴。'); return false;
    }
    return true;
  });
  if (ys.some(axis => supported.some(series => series.axisId === axis.id && series.grouping === 'percentStacked')
      && supported.some(series => series.axisId === axis.id && series.grouping !== 'percentStacked'))) {
    return { option: null, notices: ['同一原件数值轴混用了百分比堆积和普通数值，当前无法可靠重绘，请下载原件查看。'] };
  }
  const horizontal = supported.length > 0 && supported.every(series => series.type === 'bar' && series.horizontal);
  if (supported.some(series => series.horizontal) && !horizontal) {
    return { option: null, notices: ['当前图混合了不兼容的横向与纵向图组，请下载原件查看。'] };
  }
  const indexOf = (axes: typeof xs, id?: string) => Math.max(0, axes.findIndex(axis => axis.id === id));
  const categories = xs.map(axis => {
    const groups = supported.filter(series => indexOf(xs, series.categoryAxisId) === xs.indexOf(axis));
    const reference = groups.reduce<Array<string | number | null>>((best, series) => series.categories.length > best.length ? series.categories : best, []);
    if (groups.some(series => series.categories.some((label, index) => label !== null && reference[index] !== label))) notices.push('部分系列的分类标签不完全一致，保留原系列位置和各自标签。');
    return reference.map(label => label === null ? '' : String(label));
  });
  const percentKeys = new Set(supported.filter(series => series.grouping === 'percentStacked').map(series => `${series.groupId || ''}:${series.axisId || ''}`));
  const percentTotals = new Map<string, { positive: number[]; negative: number[] }>();
  for (const source of supported) {
    const key = `${source.groupId || ''}:${source.axisId || ''}`;
    if (!percentKeys.has(key)) continue;
    const total = percentTotals.get(key) || { positive: [], negative: [] }; percentTotals.set(key, total);
    source.values.forEach((value, index) => { if (typeof value === 'number' && Number.isFinite(value)) { const side = value < 0 ? total.negative : total.positive; side[index] = (side[index] || 0) + Math.abs(value); } });
  }
  const series = supported.map((source, seriesIndex) => {
    const key = `${source.groupId || ''}:${source.axisId || ''}`;
    const percent = source.grouping === 'percentStacked';
    const data = source.values.map((value, index) => {
      const raw = typeof value === 'number' && Number.isFinite(value) ? value : null;
      let plotted = raw;
      if (percent && raw !== null) {
        const totals = percentTotals.get(key)!;
        const denominator = (raw < 0 ? totals.negative : totals.positive)[index] || 0;
        plotted = denominator ? raw / denominator : 0;
      }
      const date = xs[indexOf(xs, source.categoryAxisId)]?.type === 'date';
      const time = source.categoryTimes?.[index];
      const position = typeof time === 'number' && Number.isFinite(time) ? time : null;
      return { value: date ? horizontal ? [plotted, position] : [position, plotted] : plotted,
        rawValue: raw, name: source.categories[index] === null ? '' : String(source.categories[index] ?? '') };
    });
    const stacked = source.grouping === 'stacked' || percent;
    return {
      id: String(seriesIndex), name: source.name, type: source.type === 'bar' ? 'bar' : 'line',
      xAxisIndex: horizontal ? indexOf(ys, source.axisId) : indexOf(xs, source.categoryAxisId),
      yAxisIndex: horizontal ? indexOf(xs, source.categoryAxisId) : indexOf(ys, source.axisId),
      data, connectNulls: false, showSymbol: false, smooth: source.smooth === true,
      ...(source.type === 'area' ? { areaStyle: { opacity: .24 } } : {}),
      ...(stacked ? { stack: `excel:${key}`, stackStrategy: 'samesign' } : {}),
      ...(source.color && /^#[a-f\d]{6}$/i.test(source.color) ? { itemStyle: { color: source.color }, lineStyle: { color: source.color } } : {}),
      emphasis: { focus: 'series' }, animation: false,
    };
  });
  const valueAxis = ys.map((axis, index) => {
    const percent = percentKeys.size && supported.some(source => source.axisId === axis.id && source.grouping === 'percentStacked');
    return { type: 'value', name: axis.title || '', nameTextStyle: { color: style.muted },
      position: horizontal ? axis.position === 'top' ? 'top' : 'bottom' : axis.position === 'left' ? 'left' : axis.position === 'right' ? 'right' : index % 2 ? 'right' : 'left',
      offset: Math.floor(index / 2) * 48,
      scale: !supported.some(source => indexOf(ys, source.axisId) === index && source.type !== 'line'),
      inverse: axis.inverse === true,
      ...(typeof axis.min === 'number' ? { min: axis.min } : {}), ...(typeof axis.max === 'number' ? { max: axis.max } : {}),
      axisLabel: { color: style.muted, ...(percent ? { formatter: (value: number) => `${Math.round(value * 100)}%` } : {}) },
      splitLine: { show: index === 0, lineStyle: { color: style.border } }, axisLine: { lineStyle: { color: style.border } },
    };
  });
  const categoryAxis = xs.map((axis, index) => ({ type: axis.type === 'date' ? 'time' : 'category',
    ...(axis.type === 'date' ? {} : { data: categories[index] }), name: axis.title || '', inverse: axis.inverse === true,
    boundaryGap: supported.some(source => source.type === 'bar'), axisLabel: { color: style.muted, hideOverlap: true },
    axisLine: { lineStyle: { color: style.border } }, ...(index ? { position: horizontal ? 'right' : 'top' } : {}),
  }));
  const safeText = (value: unknown) => String(value ?? '').replace(/[{}]/g, '');
  const option = {
    animation: false, backgroundColor: 'transparent', textStyle: { fontFamily: style.font, color: style.color },
    color: ['#A84427', '#3A7180', '#8B749E', '#77874D', '#C09243', '#6D8D9C', '#B66C79'],
    title: { text: chart.title, left: 12, top: 8, textStyle: { fontFamily: style.font, fontSize: 15, fontWeight: 500, color: style.color } },
    legend: { type: 'scroll', bottom: 4, left: 12, right: 12, textStyle: { color: style.muted, fontFamily: style.font }, animation: false },
    tooltip: { trigger: 'axis', renderMode: 'richText', confine: true, textStyle: { fontFamily: style.font },
      formatter: (raw: unknown) => (Array.isArray(raw) ? raw : [raw]).map((entry: { seriesName?: string; data?: { rawValue?: number | null; name?: string } }) => `${safeText(entry.data?.name)} ${safeText(entry.seriesName)}：${entry.data?.rawValue === null ? '无保存值' : safeText(entry.data?.rawValue)}`).join('\n') },
    grid: { left: 62, right: ys.length > 1 ? 72 : 28, top: 66, bottom: 80, containLabel: true },
    xAxis: horizontal ? valueAxis : categoryAxis, yAxis: horizontal ? categoryAxis : valueAxis, series,
  } as EChartsCoreOption;
  if (!series.length) notices.push('当前图没有可绘制的已保存系列。');
  return { option: series.length ? option : null, notices: [...new Set(notices)] };
}
