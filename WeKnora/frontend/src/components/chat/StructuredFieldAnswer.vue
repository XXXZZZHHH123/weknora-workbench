<template>
  <section class="structured-answer">
    <template v-if="answer.intent === 'metadata'">
      <p class="answer-label">原表字段目录 · {{ (answer.sheets || []).join('、') }}</p>
      <p class="answer-value">共 {{ answer.count }} 种：{{ (answer.groups || []).map((g:any) => g.metric).join('、') }}</p>
      <p v-for="(metrics,unit) in answer.unit_groups || {}" :key="unit">{{unit}}：{{(metrics as string[]).join('、')}}</p>
      <div v-for="group in answer.groups" :key="group.metric" class="source-result">
        <h3>{{ group.metric }}</h3>
        <p v-if="group.definition">概念说明：{{group.definition}}</p>
        <small v-for="source in group.sources" :key="source.series_id">来源：{{ answer.file }} · {{ source.sheet }} · 表头：{{ (source.header_cells || []).join('、') }}<template v-if="dimensionLabel(source.dimensions)"> · {{ dimensionLabel(source.dimensions) }}</template> · 单位：{{ source.unit || '原表未注明' }}</small>
        <small v-if="group.sources_truncated">共 {{ group.source_count }} 个来源列，以上展示部分表头证据。</small>
      </div>
      <small>{{ answer.notice }}</small>
    </template>
    <template v-else>
    <p class="answer-label">原表查询结果<span v-if="answer.date"> · {{ answer.date }}</span></p>
    <p v-if="answer.operation==='rows' && (answer.start_date || answer.end_date)">范围：{{answer.start_date || '记录起点'}} 至 {{answer.end_date || '记录终点'}}</p>
    <p v-if="answer.operation && answer.operation !== 'rows'">{{ operationLabel(answer.operation) }} · {{ answer.start_date || answer.end_date ? `${answer.start_date || '记录起点'} 至 ${answer.end_date || '记录终点'}` : '全部已导入记录' }}</p>
    <p v-if="plan?.status === 'no_data'">{{ answer.notice }}</p>
    <div v-for="(source, role) in answer.sources" :key="role" class="source-result">
      <h3><template v-if="answer.operation === 'statistics'">{{ operationLabel(source.filters?.operation) }} · </template>{{ source.series?.metric }}<template v-if="dimensionLabel(source.series?.dimensions)"> · {{ dimensionLabel(source.series?.dimensions) }}</template></h3>
      <small>来源：{{ source.source?.file }} · {{ source.source?.sheet }} · 表头：{{ (source.series?.header_cells || []).join('、') }}</small>
      <small v-if="source.series?.chart_sheet">图表位置：{{ source.series.chart_sheet }}；源指标：{{ source.series.source_metric }}</small>
      <p v-if="source.filters?.operation && !['rows','latest','max','min','top'].includes(source.filters.operation)" class="answer-value">
        {{ operationLabel(source.filters.operation) }}：{{ source.aggregate == null ? '无有效数值' : number(source.aggregate) }} {{ source.filters.operation === 'count' ? '条' : (source.series?.unit || '（原表未注明单位）') }}
      </p>
      <p v-if="!source.rows?.length && ['rows','latest','max','min'].includes(source.filters?.operation || 'rows')">指定范围内没有有效记录。</p>
      <p v-for="row in (source.rows_are === 'source_examples_not_all_inputs' ? [] : source.rows || [])" :key="row.source_cell" class="answer-value">
        <small v-if="['max','min','latest','top'].includes(source.filters?.operation)">{{ source.filters.operation === 'latest' ? '最新记录日期' : '对应日期' }}：{{ row.date || row.period || '原表未提供日期' }}</small>
        {{ row.value == null ? '无有效数值' : number(row.value) }} <span>{{ source.series?.unit || '（原表未注明单位）' }}</span>
        <small v-if="row.value == null && row.raw_value != null">原单元格内容：{{ row.raw_value }}</small>
        <small>来源：{{ source.source?.file }} · {{ source.source?.sheet }} · {{ row.source_cell }}<template v-if="row.date_cell">；日期：{{ row.date_cell }}</template><template v-if="row.period_cell">；期间：{{ row.period_cell }}</template></small>
      </p>
      <template v-if="source.filters?.operation && source.filters.operation !== 'rows'">
        <small>有效数值 {{ source.valid_count }} 条；缺失或错误 {{ source.missing_count }} 条，不按零计算。</small>
        <small v-if="source.extremum_count > 1">共有 {{ source.extremum_count }} 条并列极值记录，{{ source.truncated ? `当前展示 ${source.rows.length} 条` : '已全部列出' }}。</small>
      </template>
      <small v-if="source.rows?.some((row: any) => row.formula && row.value === 0)">结果包含原表公式保存的零值；需核验上游是否缺数，不能据此推断真实交易价格为零。</small>
      <small v-if="source.rows?.some((row: any) => row.formula)">公式结果取自原文件保存的数值，系统未重新计算整本 Excel。</small>
      <small v-if="source.filters?.last_n">按原表最近 {{ source.filters.last_n }} 个记录日期统计。</small>
      <small v-if="source.filters?.value_filters?.length">筛选条件：{{ source.filters.value_filters.map((f:any)=>`${filterLabels[f.op]} ${f.value}`).join('；') }}</small>
      <table v-if="source.window_rows?.length"><thead><tr><th>日期</th><th>原值</th><th>来源单元格</th></tr></thead><tbody><tr v-for="r in source.window_rows" :key="r.source_cell"><td>{{r.date}}</td><td>{{r.value==null?'无有效数值':number(r.value)}}</td><td>{{r.source_cell}}</td></tr></tbody></table>
      <details v-if="source.series?.chart_id && source.warnings?.length" class="chart-notice" open>
        <summary>图表范围与核验说明</summary>
        <small v-for="(warning, index) in source.warnings" :key="index">{{ warning }}</small>
      </details>
    </div>
    <p v-if="answer.explanation && answer.intent!=='calculation'">{{answer.explanation}}</p>
    <section v-if="answer.analysis" class="calculated-result">
      <h3>{{answer.analysis.label}}</h3>
      <p v-if="answer.analysis.value!=null">{{number(answer.analysis.value)}}</p>
      <p v-if="answer.analysis.first">{{answer.analysis.first.date}}：{{number(answer.analysis.first.value)}}；{{answer.analysis.last.date}}：{{number(answer.analysis.last.value)}}</p>
      <p v-if="answer.analysis.change!=null">首尾差：{{number(answer.analysis.change)}}；最大回撤：{{number(answer.analysis.max_drawdown_percent)}}%</p>
      <p v-if="answer.analysis.drawdown_sources">峰值 {{answer.analysis.drawdown_sources.peak.date}}（{{answer.analysis.drawdown_sources.peak.source_cell}}）；谷值 {{answer.analysis.drawdown_sources.trough.date}}（{{answer.analysis.drawdown_sources.trough.source_cell}}）</p>
      <p v-if="answer.analysis.last_observation">最后有效历史记录：{{answer.analysis.last_observation.date}}，{{number(answer.analysis.last_observation.value)}}（{{answer.analysis.last_observation.source_cell}}）；预测日期：{{answer.analysis.target_date}}</p>
      <p v-if="answer.analysis.interval">基线区间：{{answer.analysis.interval.map(number).join(' 至 ')}}</p>
      <small v-if="answer.analysis.sample_count">共同有效样本 / 期间有效记录：{{answer.analysis.sample_count}}</small>
      <p>{{answer.analysis.notice}}</p>
      <details v-if="answer.analysis.aligned_rows"><summary>日期对齐与来源明细</summary><table><thead><tr><th>日期</th><th>左侧原值</th><th>右侧原值</th><th>单元格</th></tr></thead><tbody><tr v-for="r in answer.analysis.aligned_rows" :key="r.date"><td>{{r.date}}</td><td>{{number(r.left_value)}}</td><td>{{number(r.right_value)}}</td><td>{{r.left_cell}} / {{r.right_cell}}</td></tr></tbody></table></details>
    </section>
    <figure v-for="chart in charts" :key="chart.label" class="source-chart">
      <figcaption>{{chart.label}} · {{chart.unit || '原表未注明单位'}}</figcaption>
      <svg viewBox="0 0 640 260" role="img" :aria-label="chart.label+'原表折线图'">
        <path :d="chart.path" fill="none" stroke="#168a55" stroke-width="2" />
        <text x="30" y="245">{{chart.first}}</text><text x="485" y="245">{{chart.last}}</text>
        <template v-for="point in chart.extrema" :key="point.label"><circle :cx="point.x" :cy="point.y" r="4" fill="#b45309"/><text :x="Math.min(point.x,450)" :y="Math.max(point.y-9,14)">{{point.label}} {{number(point.value)}}（{{point.date}}）</text></template>
      </svg>
    </figure>
    <p v-for="output in answer.outputs || []" :key="output.label">{{output.label}}：{{output.rounded_value}} {{output.unit}}</p>
    <div v-if="answer.intent === 'calculation' && answer.rounded_value != null" class="calculated-result">
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
const operationLabels: Record<string, string> = {max:'最大值',min:'最小值',mean:'算术平均值',sum:'合计',count:'有效记录数',latest:'最新有效值',statistics:'复合统计',top:'排序结果'};
const filterLabels: Record<string,string>={gt:'大于',gte:'不低于',lt:'小于',lte:'不超过',ne:'不等于'};
const number=(v:any)=>typeof v==='number'?Number(v.toPrecision(10)).toString():String(v);
const charts=computed(()=>(answer.value.chart?.series || []).map((series:any)=>{
  const rows=series.rows.filter((r:any)=>typeof r.value==='number' && r.date);
  if(!rows.length)return null;
  const values=rows.map((r:any)=>r.value);const min=Math.min(...values),max=Math.max(...values);
  const first=Date.parse(rows[0].date),last=Date.parse(rows[rows.length-1].date);
  const x=(r:any)=>30+(Date.parse(r.date)-first)/Math.max(1,last-first)*580;
  const y=(r:any)=>220-(r.value-min)/Math.max(1,max-min)*190;
  return {...series,first:rows[0].date,last:rows[rows.length-1].date,path:rows.map((r:any,i:number)=>`${i?'L':'M'}${x(r)},${y(r)}`).join(' '),extrema:[['最高',max],['最低',min]].map(([label,value])=>{const r=rows.find((r:any)=>r.value===value);return {label,value,date:r.date,x:x(r),y:y(r)};})};
}).filter(Boolean));
const operationLabel = (operation: string) => operationLabels[operation] || '查询';
const readableExpression = computed(() => String(answer.value.expression || '').replace(/\bv[1-8]\b/g, key => {
  const source = answer.value.sources?.[key];
  const value = answer.value.operation === 'statistics' && source?.filters?.operation !== 'latest' ? source?.aggregate : source?.rows?.[0]?.value;
  return value == null ? key : String(value);
}));
</script>
<style scoped>
.structured-answer { line-height: 1.75; color: var(--app-text-primary, #243d34); padding: 12px 0; }
table {width:100%;border-collapse:collapse;font-size: var(--app-text-md);} th,td {text-align:left;padding:6px;border-bottom:1px solid #dbe5de;} .source-chart {margin:18px 0;} .source-chart svg {width:100%;height:auto;font-size: var(--app-text-xs);}
.answer-label { color: #66766c; font-size: var(--app-text-md); }
h3 { font-size: var(--app-text-xl); margin: 14px 0 4px; }
.answer-value { font-size: var(--app-text-3xl); font-weight: 600; margin: 6px 0 16px; }
.answer-value span { font-size: var(--app-text-lg); font-weight: 400; }
small { display: block; font-size: var(--app-text-sm); font-weight: 400; color: #65726b; overflow-wrap: anywhere; }
.calculated-result { border-top: 1px solid #dbe5de; margin-top: 18px; padding-top: 8px; }
p { margin: 8px 0; }
</style>
