<template>
  <section class="structured-answer">
    <template v-if="answer.intent === 'metadata'">
      <p class="answer-label">原表字段目录 · {{ (answer.sheets || []).join('、') }}</p>
      <p class="answer-value">共 {{ answer.count }} 种：{{ (answer.groups || []).map((g:any) => g.metric).join('、') }}</p>
      <div v-for="group in answer.groups" :key="group.metric" class="source-result">
        <h3>{{ group.metric }}</h3>
        <small v-for="source in group.sources" :key="source.series_id">来源：{{ answer.file }} · {{ source.sheet }} · 表头：{{ (source.header_cells || []).join('、') }}<template v-if="dimensionLabel(source.dimensions)"> · {{ dimensionLabel(source.dimensions) }}</template> · 单位：{{ source.unit || '原表未注明' }}</small>
        <small v-if="group.sources_truncated">共 {{ group.source_count }} 个来源列，以上展示部分表头证据。</small>
      </div>
      <small>{{ answer.notice }}</small>
    </template>
    <template v-else>
    <p class="answer-label">原表查询结果<span v-if="answer.date"> · {{ answer.date }}</span></p>
    <p v-if="answer.operation && answer.operation !== 'rows'">{{ operationLabel(answer.operation) }} · {{ answer.start_date || answer.end_date ? `${answer.start_date || '记录起点'} 至 ${answer.end_date || '记录终点'}` : '全部已导入记录' }}</p>
    <p v-if="plan?.status === 'no_data'">{{ answer.notice }}</p>
    <div v-for="(source, role) in answer.sources" :key="role" class="source-result">
      <h3><template v-if="answer.operation === 'statistics'">{{ operationLabel(source.filters?.operation) }} · </template>{{ source.series?.metric }}<template v-if="dimensionLabel(source.series?.dimensions)"> · {{ dimensionLabel(source.series?.dimensions) }}</template></h3>
      <small v-if="source.series?.chart_sheet">图表位置：{{ source.series.chart_sheet }}；源指标：{{ source.series.source_metric }}</small>
      <p v-if="source.filters?.operation && !['rows','latest','max','min'].includes(source.filters.operation)" class="answer-value">
        {{ operationLabel(source.filters.operation) }}：{{ source.aggregate == null ? '无有效数值' : source.aggregate }} {{ source.filters.operation === 'count' ? '条' : (source.series?.unit || '（原表未注明单位）') }}
      </p>
      <p v-if="!source.rows?.length && ['rows','latest','max','min'].includes(source.filters?.operation || 'rows')">指定范围内没有有效记录。</p>
      <p v-for="row in (source.rows_are === 'source_examples_not_all_inputs' ? [] : source.rows || [])" :key="row.source_cell" class="answer-value">
        <small v-if="['max','min','latest'].includes(source.filters?.operation)">{{ source.filters.operation === 'latest' ? '最新记录日期' : '对应日期' }}：{{ row.date || row.period || '原表未提供日期' }}</small>
        {{ row.value == null ? '无有效数值' : row.value }} <span>{{ source.series?.unit || '（原表未注明单位）' }}</span>
        <small v-if="row.value == null && row.raw_value != null">原单元格内容：{{ row.raw_value }}</small>
        <small>来源：{{ source.source?.file }} · {{ source.source?.sheet }} · {{ row.source_cell }}<template v-if="row.date_cell">；日期：{{ row.date_cell }}</template><template v-if="row.period_cell">；期间：{{ row.period_cell }}</template></small>
      </p>
      <template v-if="source.filters?.operation && source.filters.operation !== 'rows'">
        <small>有效数值 {{ source.valid_count }} 条；缺失或错误 {{ source.missing_count }} 条，不按零计算。</small>
        <small v-if="source.extremum_count > 1">共有 {{ source.extremum_count }} 条并列极值记录，{{ source.truncated ? `当前展示 ${source.rows.length} 条` : '已全部列出' }}。</small>
        <small v-if="source.rows?.some((row: any) => row.formula && row.value === 0)">当前极值包含原表公式保存的零值；本次按保存值统计，需核验上游是否缺数，不能据此推断真实交易价格为零。</small>
        <small v-if="source.rows?.some((row: any) => row.formula)">公式结果取自原文件保存的数值，系统未重新计算整本 Excel。</small>
      </template>
      <details v-if="source.series?.chart_id && source.warnings?.length" class="chart-notice" open>
        <summary>图表范围与核验说明</summary>
        <small v-for="(warning, index) in source.warnings" :key="index">{{ warning }}</small>
      </details>
    </div>
    <div v-if="answer.intent === 'calculation'" class="calculated-result">
      <h3>{{ answer.operation === 'statistics' ? '统计计算结果' : '计算结果' }}：{{ answer.rounded_value }} {{ answer.unit }}</h3>
      <p>{{ answer.explanation }}</p>
      <p>计算式：{{ readableExpression }}</p>
      <small>{{ answer.operation === 'statistics' ? '极值与差值由程序按同一范围计算，请结合原表来源核对。' : '上述数值由程序计算。换算公式和业务口径由模型提出，请结合来源核对。' }}</small>
    </div>
    </template>
  </section>
</template>
<script setup lang="ts">
import { computed } from 'vue';
import type { Clarification } from '@/utils/fieldClarification';
import { dimensionLabel } from '@/utils/fieldClarification';
const props = defineProps<{ plan: Clarification | null }>();
const answer = computed(() => props.plan?.answer || {});
const operationLabels: Record<string, string> = {max:'最大值',min:'最小值',mean:'算术平均值',sum:'合计',count:'有效记录数',latest:'最新有效值',statistics:'复合统计'};
const operationLabel = (operation: string) => operationLabels[operation] || '查询';
const readableExpression = computed(() => String(answer.value.expression || '').replace(/\bv[1-8]\b/g, key => {
  const source = answer.value.sources?.[key];
  const value = answer.value.operation === 'statistics' && source?.filters?.operation !== 'latest' ? source?.aggregate : source?.rows?.[0]?.value;
  return value == null ? key : String(value);
}));
</script>
<style scoped>
.structured-answer { line-height: 1.75; color: var(--app-text-primary, #243d34); padding: 12px 0; }
.answer-label { color: #66766c; font-size: 13px; }
h3 { font-size: 16px; margin: 14px 0 4px; }
.answer-value { font-size: 21px; font-weight: 600; margin: 6px 0 16px; }
.answer-value span { font-size: 15px; font-weight: 400; }
small { display: block; font-size: 12px; font-weight: 400; color: #65726b; overflow-wrap: anywhere; }
.calculated-result { border-top: 1px solid #dbe5de; margin-top: 18px; padding-top: 8px; }
p { margin: 8px 0; }
</style>
