<script setup lang="ts">
import type { SourceFileVersionChoice } from '../sourceFileVersion'

defineProps<{ choice: SourceFileVersionChoice; knowledgeId: string }>()
const emit = defineEmits<{ 'update:knowledgeId': [value: string] }>()
</script>

<template>
  <label class="upload-version-choice">
    <span>{{ $t('knowledgeBase.uploadAsFileVersion', { fileName: choice.fileName }) }}</span>
    <select :value="knowledgeId" @change="emit('update:knowledgeId', ($event.target as HTMLSelectElement).value)">
      <option v-for="candidate in choice.candidates" :key="candidate.id" :value="candidate.id">
        {{ $t('common.yes') }}<template v-if="candidate.folder_path || choice.candidates.length > 1"> · {{ candidate.folder_path || $t('knowledgeBase.folderTree.rootRow') }}<template v-if="choice.candidates.length > 1 && candidate.created_at"> · {{ new Date(candidate.created_at).toLocaleString() }}</template></template>
      </option>
      <option value="">{{ $t('knowledgeBase.saveFileAsNew') }}</option>
    </select>
  </label>
</template>

<style scoped lang="less">
.upload-version-choice {
  display: grid;
  gap: 6px;
  overflow-wrap: anywhere;

  select {
    min-width: 0;
    padding: 6px 8px;
    color: var(--td-text-color-primary);
    background: var(--td-bg-color-container);
    border: 1px solid var(--td-component-stroke);
    border-radius: var(--app-radius-xs);
  }
}
</style>
