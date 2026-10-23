<script setup lang="ts">
import { nextTick, onMounted, onUnmounted, ref, watch } from 'vue';
import type { EChartsType } from 'echarts/core';
import type { ExcelPreviewChart } from '@/utils/excelPreviewCharts';
import { excelSourceChartOption } from '@/utils/excelSourceChart';

const props = defineProps<{ chart: ExcelPreviewChart }>();
const container = ref<HTMLElement | null>(null), error = ref(''), loading = ref(false);
let instance: EChartsType | null = null, resize: ResizeObserver | null = null, theme: MutationObserver | null = null;
let version = 0, alive = true;
function dispose() { resize?.disconnect(); resize = null; instance?.dispose(); instance = null; }
async function render() {
  const own = ++version; dispose(); loading.value = true; error.value = '';
  try {
    const engine = await import('@/utils/excelChartEngine'); await nextTick();
    if (!alive || own !== version || !container.value) return;
    const style = getComputedStyle(container.value), root = getComputedStyle(document.documentElement);
    const result = excelSourceChartOption(props.chart, { color: root.getPropertyValue('--td-text-color-primary').trim() || '#292723', muted: root.getPropertyValue('--td-text-color-secondary').trim() || '#73726c', border: root.getPropertyValue('--td-component-stroke').trim() || '#e6e5e2', font: style.fontFamily }, { connectGaps: true });
    if (!result.option) { error.value = '这张图暂无法预览。'; return; }
    instance = engine.init(container.value, undefined, { renderer: 'canvas', devicePixelRatio: Math.min(2, window.devicePixelRatio || 1) });
    instance.setOption(result.option, { notMerge: true });
    resize = new ResizeObserver(() => instance?.resize()); resize.observe(container.value);
  } catch { if (alive && own === version) error.value = '图表未能绘制，请重试或下载原件查看。'; }
  finally { if (alive && own === version) loading.value = false; }
}
watch(() => props.chart, render);
onMounted(() => { void render(); theme = new MutationObserver(() => void render()); theme.observe(document.documentElement, { attributes: true, attributeFilter: ['theme-mode'] }); });
onUnmounted(() => { alive = false; version++; dispose(); theme?.disconnect(); });
</script>

<template>
  <section class="excel-source-chart" :aria-label="chart.title || '原件图表'" :aria-busy="loading">
    <p v-if="loading" class="chart-notice" role="status">正在绘制原件图表…</p>
    <p v-if="error" class="chart-notice" role="alert">{{ error }} <button @click="render">重试</button></p>
    <div ref="container" class="chart-canvas" role="img" :aria-label="`${chart.title || '原件图表'}，${chart.series.length} 个已保存系列`" />
  </section>
</template>

<style scoped>
.excel-source-chart { min-height: 0; flex: 1; overflow: auto; font-family: inherit; color: inherit; }
.chart-canvas { width: 100%; height: min(56vh, 520px); min-height: 300px; }
.chart-notice { margin: 0; padding: 6px 12px; font-size: var(--app-text-sm, 12px); color: var(--td-text-color-secondary); line-height: 1.5; }
button { font: inherit; cursor: pointer; border: 1px solid var(--td-component-stroke); border-radius: 5px; color: inherit; background: var(--td-bg-color-container); }
</style>
