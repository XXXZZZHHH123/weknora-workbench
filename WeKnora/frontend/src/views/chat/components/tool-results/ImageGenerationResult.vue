<template>
  <div class="image-generation-result">
    <p v-if="success === false" class="image-generation-error" role="alert">{{ output || $t('imageGeneration.failed') }}</p>
    <template v-else-if="delivery">
      <p class="image-generation-summary">{{ $t('imageGeneration.completed') }}<span v-if="dimensions"> · {{ dimensions }}</span></p>
      <p v-if="loading" class="image-generation-hint" role="status">{{ $t('imageGeneration.loading') }}</p>
      <p v-if="loadFailed" class="image-generation-error" role="alert">
        {{ $t('imageGeneration.loadFailed') }}
        <button type="button" class="image-generation-action" @click="loadImages">{{ $t('imageGeneration.retry') }}</button>
      </p>
      <figure v-for="artifact in delivery.artifacts" :key="artifact.index" class="generated-image">
        <button v-if="imageURLs[artifact.index]" type="button" class="generated-image-preview"
          :aria-label="$t('imageGeneration.preview')" @click="previewURL = imageURLs[artifact.index] || ''">
          <img :src="imageURLs[artifact.index]" :alt="$t('imageGeneration.generatedImage')" />
        </button>
        <figcaption>
          <span>{{ artifact.file_name }}</span>
          <button type="button" class="image-generation-action" :disabled="!imageURLs[artifact.index]"
            @click="downloadImage(artifact)">{{ $t('imageGeneration.download') }}</button>
        </figcaption>
      </figure>
      <t-dialog :visible="!!previewURL" :header="$t('imageGeneration.preview')" :footer="false" width="min(92vw, 1100px)"
        @update:visible="(value: boolean) => { if (!value) previewURL = '' }">
        <img v-if="previewURL" class="generated-image-full" :src="previewURL" :alt="$t('imageGeneration.generatedImage')" />
      </t-dialog>
    </template>
    <p v-else class="image-generation-hint">{{ $t('imageGeneration.unavailable') }}</p>
  </div>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { generatedImageDelivery, loadGeneratedImage, type GeneratedImageArtifact } from '@/utils/imageGenerationResult'

const props = defineProps<{ data: Record<string, unknown>; success?: boolean; output?: string }>()
const delivery = computed(() => generatedImageDelivery(props.data || {}))
const imageURLs = ref<Record<number, string>>({})
const previewURL = ref('')
const loading = ref(false)
const loadFailed = ref(false)
const dimensions = computed(() => {
  const width = Number(props.data?.width), height = Number(props.data?.height)
  return width > 0 && height > 0 ? `${width} × ${height}` : ''
})
let mounted = false
let revision = 0

function releaseImages() {
  previewURL.value = ''
  for (const url of Object.values(imageURLs.value)) URL.revokeObjectURL(url)
  imageURLs.value = {}
}

async function loadImages() {
  const current = ++revision
  releaseImages()
  loadFailed.value = false
  const target = delivery.value
  if (!target || props.success === false) { loading.value = false; return }
  loading.value = true
  try {
    // Uses the existing authenticated artifact endpoint; never a provider's URL.
    const { downloadArtifact } = await import('@/api/chat')
    await Promise.all(target.artifacts.map(async artifact => {
      try {
        const blob = await loadGeneratedImage(target, artifact, downloadArtifact)
        if (current !== revision) return
        imageURLs.value = { ...imageURLs.value, [artifact.index]: URL.createObjectURL(blob) }
      } catch {
        if (current === revision) loadFailed.value = true
      }
    }))
  } catch {
    if (current === revision) loadFailed.value = true
  } finally {
    if (current === revision) loading.value = false
  }
}

function downloadImage(artifact: GeneratedImageArtifact) {
  const url = imageURLs.value[artifact.index]
  if (!url) return
  const link = document.createElement('a')
  link.href = url
  link.download = artifact.file_name
  link.click()
}

onMounted(() => { mounted = true; void loadImages() })
watch(() => [props.data, props.success], () => { if (mounted) void loadImages() })
onBeforeUnmount(() => { mounted = false; ++revision; releaseImages() })
</script>

<style lang="less" scoped>
.image-generation-result { display: grid; gap: 10px; min-width: 0; }
.image-generation-summary, .image-generation-hint, .image-generation-error { margin: 0; font-size: var(--app-text-sm); }
.image-generation-hint { color: var(--td-text-color-secondary); }
.image-generation-error { color: var(--td-error-color); }
.generated-image { margin: 0; max-width: 480px; }
.generated-image-preview { padding: 0; border: 0; border-radius: var(--app-radius-sm); overflow: hidden; cursor: zoom-in; background: var(--td-bg-color-secondarycontainer); }
.generated-image-preview img { display: block; max-width: 100%; max-height: 360px; object-fit: contain; }
.generated-image figcaption { display: flex; align-items: center; justify-content: space-between; gap: 12px; overflow-wrap: anywhere; font-size: var(--app-text-sm); }
.image-generation-action { border: 0; padding: 6px 0; background: transparent; color: var(--td-brand-color); cursor: pointer; white-space: nowrap; }
.image-generation-action:disabled { color: var(--td-text-color-disabled); cursor: default; }
.generated-image-full { display: block; max-width: 100%; max-height: 75vh; margin: auto; object-fit: contain; }
</style>
