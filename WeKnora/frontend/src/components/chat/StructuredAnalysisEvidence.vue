<template>
  <details v-if="evidence" class="analysis-evidence">
    <summary>查看分析取数与计算依据</summary>
    <p>来源：{{ evidence.source?.file }}。本次计算读取 {{ evidence.input_count }} 条记录。</p>
    <p v-for="(input,id) in evidence.inputs" :key="String(id)">
      {{ input.series.sheet }} · {{ input.series.metric }}
      <span>（{{ input.series.unit || '原表未注明单位' }}）</span><br />
      实际记录：{{ input.actual_first_date || '无日期' }} 至 {{ input.actual_last_date || '无日期' }}，有效 {{ input.valid_count }} 条。
      表头：{{ input.series.header_cells?.join('、') }}。
    </p>
    <div class="analysis-table"><table>
      <thead><tr><th v-for="(column,i) in evidence.columns" :key="i">{{ column }}</th></tr></thead>
      <tbody><tr v-for="(row,i) in evidence.rows" :key="i"><td v-for="(value,j) in row" :key="j">{{ value === null ? '无有效值' : value }}</td></tr></tbody>
    </table></div>
    <p v-if="evidence.output_truncated">仅展示部分结果行，计算使用了上述全部输入。</p>
    <details><summary>查看计算表达式</summary><pre>{{ evidence.sql }}</pre></details>
  </details>
</template>
<script setup lang="ts">
defineProps<{evidence:any}>();
</script>
<style scoped>
.analysis-evidence { margin: 12px 0; padding: 12px; border: 1px solid #dce5df; border-radius: 8px; line-height: 1.7; }
summary { cursor: pointer; } .analysis-table { overflow-x: auto; }
table { border-collapse: collapse; width: 100%; } th,td { padding: 6px 10px; border: 1px solid #dce5df; text-align: left; }
pre { white-space: pre-wrap; overflow-wrap: anywhere; }
</style>
