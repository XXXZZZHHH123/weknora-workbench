<script setup lang="ts">
import { defineAsyncComponent, h, onMounted, onUnmounted, ref, shallowRef } from 'vue'
import { useI18n } from 'vue-i18n'
import { RESOURCE_PREVIEW_EVENT, type LoadedProtectedFile } from '@/utils/protectedResource'

const file = shallowRef<LoadedProtectedFile | null>(null)
const visible = ref(false)
const version = ref(0)
const { t } = useI18n()
function makeDocumentPreview(): ReturnType<typeof defineAsyncComponent> {
  return defineAsyncComponent({
    loader: () => import('./document-preview.vue'),
    delay: 120,
    timeout: 30000,
    loadingComponent: { setup: () => () => h('div', { class: 'protected-preview-status', role: 'status' }, t('preview.loading')) },
    errorComponent: { setup: () => () => h('div', { class: 'protected-preview-status', role: 'alert' }, [
      h('span', t('preview.loadFailed')),
      h('button', { type: 'button', onClick: () => { DocumentPreview.value = makeDocumentPreview() } }, t('preview.retry')),
    ]) },
  })
}
const DocumentPreview = shallowRef(makeDocumentPreview())
function open(event: Event) {
  const detail = (event as CustomEvent<LoadedProtectedFile>).detail
  if (!detail || !(detail.blob instanceof Blob) || !detail.blobURL?.startsWith('blob:')) return
  event.preventDefault()
  file.value = detail
  version.value++
  visible.value = true
}
onMounted(() => window.addEventListener(RESOURCE_PREVIEW_EVENT, open))
onUnmounted(() => window.removeEventListener(RESOURCE_PREVIEW_EVENT, open))
</script>

<template>
  <t-drawer v-model:visible="visible" :header="file?.fileName" size="70%"
    :z-index="2200" attach="body" :footer="false" destroy-on-close>
    <template v-if="file && visible">
      <a :href="file.blobURL" :download="file.fileName">{{ $t('agent.artifactDrawer.download') }}</a>
      <DocumentPreview :key="version" :source-blob="file.blob" :file-name="file.fileName"
        :file-type="file.blob.type" :active="visible" />
    </template>
  </t-drawer>
</template>

<style>
.protected-resource-card {
  display: inline-flex;
  align-items: center;
  max-width: 100%;
  padding: 14px 18px;
  border: 1px solid var(--td-component-border);
  border-radius: var(--app-radius-lg);
  color: var(--td-text-color-primary);
  background: var(--td-bg-color-container);
  overflow-wrap: anywhere;
  text-decoration: none;
}
.protected-resource-card::before { content: '↗'; margin-right: 12px; }
.protected-resource-card:hover { border-color: var(--td-brand-color); }
.protected-preview-status {
  display: flex;
  gap: 12px;
  align-items: center;
  padding: 24px;
  color: var(--td-text-color-secondary);
}
.protected-preview-status button {
  border: 1px solid var(--td-component-border);
  border-radius: var(--app-radius-md);
  padding: 5px 12px;
  color: var(--td-text-color-primary);
  background: var(--td-bg-color-container);
  cursor: pointer;
}
</style>
