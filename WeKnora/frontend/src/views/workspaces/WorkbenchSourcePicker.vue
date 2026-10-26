<script setup lang="ts">
import type { WorkbenchSourceInput } from '@/api/workspaces'
import { MAX_WORKBENCH_SOURCES, sourceKey } from '@/views/research/workbenchDataSources'

const props = defineProps<{ modelValue: WorkbenchSourceInput[]; options: Array<WorkbenchSourceInput & { name: string; hint?: string; disabled?: boolean }>; disabled?: boolean }>()
const emit = defineEmits<{ 'update:modelValue': [sources: WorkbenchSourceInput[]] }>()
function toggle(source: WorkbenchSourceInput, checked: boolean) {
  if (props.disabled) return
  const selected = props.modelValue.some(item => sourceKey(item) === sourceKey(source))
  if (checked && !selected && props.modelValue.length < MAX_WORKBENCH_SOURCES) emit('update:modelValue', [...props.modelValue, { source_type: source.source_type, source_id: source.source_id }])
  else if (!checked && selected) emit('update:modelValue', props.modelValue.filter(item => sourceKey(item) !== sourceKey(source)))
}
</script>

<template>
  <fieldset class="workbench-source-picker" :disabled="disabled">
    <legend class="sr-only">工作台数据来源</legend>
    <label v-for="source in options" :key="sourceKey(source)" :class="{ 'is-selected': modelValue.some(item => sourceKey(item) === sourceKey(source)) }">
      <input type="checkbox" :checked="modelValue.some(item => sourceKey(item) === sourceKey(source))" :disabled="(source.disabled || modelValue.length >= MAX_WORKBENCH_SOURCES) && !modelValue.some(item => sourceKey(item) === sourceKey(source))" @change="toggle(source, ($event.target as HTMLInputElement).checked)" />
      <t-icon :name="source.source_type === 'central_dataset' ? 'database' : 'folder'" size="15px" />
      <span>{{ source.name }}</span><small>{{ source.hint }}</small>
    </label>
    <p v-if="!options.length">暂无可关联的来源，可以先创建工作台，稍后添加。</p>
  </fieldset>
</template>

<style scoped>
.workbench-source-picker{padding:0;margin:0;border:0;max-height:240px;overflow:auto;min-width:0}
.workbench-source-picker label{display:flex;align-items:center;gap:7px;padding:7px 8px;margin:2px 0;border-radius:var(--app-radius-sm);font-size:var(--app-text-sm);cursor:pointer}
.workbench-source-picker label:hover,.workbench-source-picker .is-selected{background:var(--td-bg-color-container-hover)}
.workbench-source-picker input{margin:0;accent-color:var(--td-brand-color);flex:none}
.workbench-source-picker span{min-width:0;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}
.workbench-source-picker small{margin-left:auto;flex:none;color:var(--app-text-secondary);font-size:var(--app-text-xs)}
.workbench-source-picker p{margin:7px 0;color:var(--app-text-secondary);font-size:var(--app-text-sm)}
.sr-only{position:absolute;width:1px;height:1px;padding:0;overflow:hidden;clip:rect(0,0,0,0);white-space:nowrap;border:0}
</style>
