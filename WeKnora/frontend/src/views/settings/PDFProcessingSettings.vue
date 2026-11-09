<template>
  <section v-if="isAdmin" class="pdf-processing-settings" data-testid="pdf-processing-settings">
    <div class="pdf-processing-heading">
      <h3>{{ t('pdfProcessing.title') }}</h3>
      <span>{{ t('pdfProcessing.timingHint') }}</span>
    </div>
    <p v-if="error" role="alert" class="pdf-processing-error">{{ error }} <t-button v-if="!loaded" variant="text" @click="load">{{ t('pdfProcessing.retry') }}</t-button></p>
    <div class="pdf-processing-fields">
      <label>{{ t('pdfProcessing.overlap') }}
        <t-input-number v-model="form.overlap" :min="1" :max="5" :step="1" :disabled="!editable" :suffix="t('pdfProcessing.pages')" :aria-label="t('pdfProcessing.overlap')" />
      </label>
      <label>{{ t('pdfProcessing.batch') }}
        <t-input-number v-model="form.batch" :min="12" :max="96" :step="1" :disabled="!editable" suffix="KB" :aria-label="t('pdfProcessing.batch')" />
      </label>
      <label>{{ t('pdfProcessing.queue') }}
        <t-input-number v-model="form.queue" :min="1" :max="24" :step="1" :disabled="!editable" :suffix="t('pdfProcessing.hours')" :aria-label="t('pdfProcessing.queue')" />
      </label>
      <label>{{ t('pdfProcessing.execution') }}
        <t-input-number v-model="form.execution" :min="10" :max="120" :step="1" :disabled="!editable" :suffix="t('pdfProcessing.minutes')" :aria-label="t('pdfProcessing.execution')" />
      </label>
    </div>
    <div class="pdf-processing-actions">
      <span role="status">{{ saved ? t('pdfProcessing.saved') : t('pdfProcessing.applyHint') }}</span>
      <t-button variant="text" :disabled="!editable" @click="restore">{{ t('pdfProcessing.restore') }}</t-button>
      <t-button :loading="saving" :disabled="!editable || !dirty" @click="save">{{ t('pdfProcessing.save') }}</t-button>
    </div>
  </section>
</template>

<script setup lang="ts">
import { computed, onMounted, reactive, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useAuthStore } from '@/stores/auth'
import { getPDFProcessingSettings, updatePDFProcessingSettings, type PDFProcessingSettings } from '@/api/pdf-processing'

const { t } = useI18n()
const auth = useAuthStore()
const isAdmin = computed(() => auth.hasRole('admin'))
const form = reactive({ overlap: 1, batch: 24, queue: 24, execution: 120 })
const defaults = ref<PDFProcessingSettings | null>(null)
const loaded = ref(false), saving = ref(false), canEdit = ref(false), error = ref(''), saved = ref(false)
const persisted = ref('')
let generation = 0
const editable = computed(() => isAdmin.value && loaded.value && canEdit.value && !saving.value)
const values = (): PDFProcessingSettings => ({ overlap_pages: form.overlap, grouping_request_bytes: form.batch * 1000,
  queue_timeout_seconds: form.queue * 3600, execution_timeout_seconds: form.execution * 60 })
const dirty = computed(() => JSON.stringify(values()) !== persisted.value)
function apply(settings: PDFProcessingSettings) {
  form.overlap = settings.overlap_pages; form.batch = settings.grouping_request_bytes / 1000
  form.queue = settings.queue_timeout_seconds / 3600; form.execution = settings.execution_timeout_seconds / 60
}
function message(e: any) { return e?.error?.message || e?.message || t('pdfProcessing.failed') }
async function load() {
  const current = ++generation
  loaded.value = false; canEdit.value = false; error.value = ''; saved.value = false
  if (!isAdmin.value) return
  try {
    const result = await getPDFProcessingSettings()
    if (current !== generation) return
    apply(result.data); defaults.value = result.defaults; persisted.value = JSON.stringify(result.data)
    canEdit.value = result.can_edit; loaded.value = true
  } catch (e) { if (current === generation) error.value = message(e) }
}
function restore() {
  if (editable.value && defaults.value) { apply(defaults.value); saved.value = false }
}
async function save() {
  if (!editable.value || !dirty.value) return
  const current = generation
  const policy = values()
  if (!Object.values(policy).every(Number.isInteger) || policy.overlap_pages < 1 || policy.overlap_pages > 5 ||
      policy.grouping_request_bytes < 12000 || policy.grouping_request_bytes > 96000 ||
      policy.queue_timeout_seconds < 3600 || policy.queue_timeout_seconds > 86400 ||
      policy.execution_timeout_seconds < 600 || policy.execution_timeout_seconds > 7200) { error.value = t('pdfProcessing.invalid'); return }
  saving.value = true; error.value = ''; saved.value = false
  try {
    const result = await updatePDFProcessingSettings(policy)
    if (current !== generation) return
    apply(result.data); persisted.value = JSON.stringify(result.data); saved.value = true
  } catch (e) { if (current === generation) error.value = message(e) }
  finally { saving.value = false }
}
watch(() => auth.effectiveTenantId, load)
onMounted(load)
</script>

<style scoped>
.pdf-processing-settings { margin: 24px 0 8px; padding-top: 24px; border-top: 1px solid var(--td-component-stroke); }
.pdf-processing-heading { display: flex; align-items: baseline; gap: 16px; flex-wrap: wrap; margin-bottom: 20px; }
.pdf-processing-heading h3 { margin: 0; font-size: var(--app-text-lg); font-weight: 600; }
.pdf-processing-heading span, .pdf-processing-actions span { color: var(--td-text-color-secondary); font-size: var(--app-text-sm); }
.pdf-processing-fields { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 18px 32px; }
.pdf-processing-fields label { display: flex; justify-content: space-between; align-items: center; gap: 12px; font-size: var(--app-text-md); }
.pdf-processing-fields :deep(.t-input-number) { width: 150px; flex-shrink: 0; }
.pdf-processing-actions { display: flex; gap: 12px; align-items: center; margin-top: 22px; flex-wrap: wrap; }
.pdf-processing-actions span { margin-right: auto; }
.pdf-processing-error { color: var(--td-error-color); font-size: var(--app-text-md); }
@media (max-width: 650px) { .pdf-processing-fields { grid-template-columns: 1fr; } .pdf-processing-heading { gap: 6px; } }
</style>
