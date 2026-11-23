<script setup lang="ts">
import { computed, ref, shallowRef, watch, onBeforeUnmount, nextTick } from 'vue'
import { useI18n } from 'vue-i18n'
import { useAuthStore } from '@/stores/auth'
import DocumentPreview from '@/components/document-preview.vue'
import { getMessageEvidence, previewEvidenceSource, type EvidenceDetail, type EvidenceReference, type EvidenceUnit } from '@/api/chat/evidence'
import { evidenceCellPosition, evidenceSourceLabel, evidenceSourceLocate, evidenceTableColumnInfo, evidenceUnitSymbol, findEvidenceReference } from '@/utils/evidenceSources'

const props = defineProps<{ sessionId: string; messageId: string; references?: EvidenceReference[] }>()
const { t } = useI18n()
const auth = useAuthStore()
const admin = computed(() => auth.hasRole?.('admin') === true)
const visible = ref(false), busy = ref(false), error = ref(''), selected = ref('')
const detail = shallowRef<EvidenceDetail | null>(null)
const original = shallowRef<Blob | null>(null)
const sourceIndex = ref(0), previewBusy = ref(false), previewError = ref('')
const position = ref(''), token = ref(0)
const panel = ref<HTMLElement | null>(null)
let previousFocus: HTMLElement | null = null
let generation = 0
const source = computed(() => detail.value?.sources?.[sourceIndex.value])
const columnInfo = computed(() => detail.value ? evidenceTableColumnInfo(detail.value) : [])
const columns = computed(() => columnInfo.value.map(column => column.label))
const rows = computed(() => detail.value?.manifest?.result?.rows || [])
const locate = computed(() => detail.value ? evidenceSourceLocate(detail.value, sourceIndex.value, token.value) : null)
const globalUnit = computed(() => detail.value?.manifest?.unit?.status === 'per_column' ? undefined : detail.value?.manifest?.unit)
const unitDeclarations = computed(() => {
  const perColumn = columnInfo.value.filter(column => column.unit).map(column => ({ label: column.label, unit: column.unit! }))
  return perColumn.length ? perColumn : globalUnit.value ? [{ label: t('evidence.unit'), unit: globalUnit.value }] : []
})
const isCalculation = computed(() => detail.value?.reference.kind === 'structured_result')
const isUnverified = computed(() => detail.value?.reference.review_status === 'unverified')
const hasUnverifiedReferences = computed(() => props.references?.some(reference => reference.review_status === 'unverified'))
const computation = computed(() => detail.value?.manifest?.computation)
const scopeText = computed(() => {
  switch (computation.value?.input_scope) {
    case 'loaded_inputs_before_user_sql':
    case 'loaded_returned_rows':
    case 'actual_loaded_rows':
      return t('evidence.loadedInputs', 'Records read before filtering and calculation')
    case 'returned_source_rows':
      return t('evidence.returnedSourceRows', 'Records returned by this query')
    default: return ''
  }
})
const chartAxes = computed(() => {
  if (!computation.value?.mappings?.length && !detail.value?.reference.artifact_ids?.length) return []
  return columnInfo.value.map(column => {
    const unit = evidenceUnitSymbol(column.unit)
    return unit ? `${column.label} (${unit})` : column.label
  })
})
const sourceLabels = computed(() => detail.value?.sources?.map((item, index) => {
  const positions = evidenceSourceLocate(detail.value!, index, 0).locators.map(evidenceSourceLabel).filter(Boolean)
  return [item.title || item.file_name, ...new Set(positions)].filter(Boolean).join(' · ')
}) || [])
const unavailableCopy = computed(() => {
  switch (detail.value?.message) {
    case '历史证据未保存原件版本，无法确认原定位':
      return t('evidence.historicalVersionMissing', 'This older answer did not retain the original file version, so the passage cannot be located.')
    case '原件版本核验超过单次预览范围':
      return t('evidence.previewLimit', 'The full file could not be checked in this preview. Open the original file from the knowledge base.')
    case '来源已更新或原件版本无法读取，原定位不可用':
      return t('evidence.sourceVersionChanged', 'The file version used for this answer could not be opened. Please select the source again and check the citation.')
    default: return t('evidence.unavailable')
  }
})
const hasComputation = computed(() => !!computation.value && (computation.value.expression || computation.value.sql || computation.value.input_scope !== undefined || computation.value.input_count !== undefined || computation.value.coverage !== undefined || computation.value.mappings?.length || computation.value.source_truncated || computation.value.input_complete === false))
function unitText(unit?: EvidenceUnit): string {
  const status = unit?.status === 'declared' ? t('evidence.declaredUnit') : unit?.status === 'derived' ? t('evidence.derivedUnit') : unit?.status === 'mixed' ? t('evidence.mixedUnits') : t('evidence.unknownUnit')
  const symbol = evidenceUnitSymbol(unit)
  return symbol ? `${symbol} · ${status}` : status
}
function unitLevel(unit: EvidenceUnit): string {
  const keys: Record<string, string> = { reviewed_plan_declaration: 'reviewedPlanUnit', imported_series_metadata: 'importedSeriesUnit', deterministic_expression: 'expressionUnit' }
  const key = unit.evidence_level && Object.hasOwn(keys, unit.evidence_level) ? keys[unit.evidence_level] : undefined
  return key ? t(`evidence.${key}`) : t('evidence.unitBasisUnknown', 'The unit basis was not recorded. Please check the original table.')
}
const cellPosition = computed(() => {
  const cells = detail.value?.manifest?.source_cells || []
  if (!cells.length) {
    const locators = detail.value?.locators || []
    return locators.length && locators.every(locator => locator.type === 'sheet' && (locator.column_start || 0) > 0) ? t('evidence.cellLocation') : ''
  }
  const kinds = new Set(cells.map(evidenceCellPosition))
  if (kinds.has('unavailable')) return t('evidence.noCoordinates')
  if (kinds.has('page')) return t('evidence.pageLocation')
  if (kinds.has('region')) return t('evidence.regionLocation')
  return t('evidence.cellLocation')
})

function close() {
  generation++; visible.value = false; busy.value = false; previewBusy.value = false
  detail.value = null; original.value = null; error.value = ''; previewError.value = ''; position.value = ''
  previousFocus?.focus(); previousFocus = null
}
async function open(id = props.references?.[0]?.id || '') {
  if (!props.sessionId || !props.messageId || !findEvidenceReference(props.references, id)) return
  if (!visible.value) previousFocus = document.activeElement instanceof HTMLElement ? document.activeElement : null
  const own = ++generation
  selected.value = id; visible.value = true; busy.value = true; error.value = ''; previewError.value = ''; original.value = null; detail.value = null; position.value = ''
  void nextTick(() => panel.value?.focus())
  try {
    const value = await getMessageEvidence(props.sessionId, props.messageId, id)
    if (own !== generation) return
    if (value.reference.id !== id) throw new Error('Evidence identity mismatch')
    detail.value = value; sourceIndex.value = 0; token.value++
  } catch {
    if (own === generation) error.value = t('evidence.loadFailed')
  } finally { if (own === generation) busy.value = false }
}
function onKeydown(event: KeyboardEvent) {
  if (event.key === 'Escape') { event.preventDefault(); close(); return }
  if (event.key !== 'Tab' || !panel.value) return
  const items = [...panel.value.querySelectorAll<HTMLElement>('button:not(:disabled), a[href], input, select, textarea, [tabindex="0"]')].filter(item => item.getClientRects().length)
  const first = items[0], last = items.at(-1)
  if (!first || !last) { event.preventDefault(); panel.value.focus(); return }
  if (event.shiftKey && (document.activeElement === first || document.activeElement === panel.value)) { event.preventDefault(); last.focus() }
  else if (!event.shiftKey && document.activeElement === last) { event.preventDefault(); first.focus() }
}
async function openOriginal(index = sourceIndex.value) {
  if (!detail.value || detail.value.status !== 'snapshot' || !detail.value.sources?.[index]) return
  const own = ++generation
  sourceIndex.value = index; original.value = null; position.value = ''; previewError.value = ''; previewBusy.value = true
  try {
    const bytes = await previewEvidenceSource(props.sessionId, props.messageId, selected.value, index)
    if (own === generation) { original.value = bytes; token.value++ }
  } catch { if (own === generation) previewError.value = t('evidence.previewFailed') }
  finally { if (own === generation) previewBusy.value = false }
}
function onLocated(value: { found: boolean; precise: boolean; page?: number }) {
  position.value = value.found ? cellPosition.value || t('evidence.pageLocation') : t('evidence.noCoordinates')
}
function displayCell(cell: unknown): string {
  if (cell == null) return '—'
  if (typeof cell === 'boolean') return t(cell ? 'common.yes' : 'common.no')
  if (typeof cell !== 'object') return String(cell)
  if (Array.isArray(cell)) return cell.map(displayCell).join(' · ')
  const value = cell as Record<string, unknown>
  const parts = [
    [t('evidence.cellValue', 'Value'), value.value],
    [t('evidence.unit'), value.unit],
    [t('evidence.cellSource', 'Source'), value.source_cell],
  ].filter(([, item]) => ['string', 'number'].includes(typeof item))
  return parts.length
    ? parts.map(([label, item]) => `${label} · ${item}`).join(' / ')
    : t('evidence.cellOpenOriginal', 'See the original file')
}

watch(() => [props.sessionId, props.messageId, auth.token, auth.selectedTenantId], close)
onBeforeUnmount(close)
defineExpose({ open })
</script>

<template>
  <div v-if="references?.length" class="message-evidence">
    <button type="button" class="message-evidence__open" @click="open()">{{ t(hasUnverifiedReferences ? 'evidence.checkingButton' : 'evidence.button') }} · {{ references.length }}</button>
    <Teleport to="body">
      <div v-if="visible" class="message-evidence__backdrop" @click.self="close">
        <section ref="panel" tabindex="-1" class="message-evidence__panel" role="dialog" aria-modal="true" :aria-label="t('evidence.title')" @keydown="onKeydown">
          <header><h3>{{ t('evidence.title') }}</h3><button type="button" @click="close">{{ t('common.close') }}</button></header>
          <div class="message-evidence__tabs" v-if="references.length > 1">
            <button v-for="(item, index) in references" :key="item.id" type="button" :aria-pressed="item.id === selected" @click="open(item.id)">{{ item.title || `${t('evidence.result')} ${index + 1}` }}</button>
          </div>
          <p v-if="busy" role="status">{{ t('evidence.loading') }}</p>
          <p v-else-if="error" role="alert">{{ error }}</p>
          <template v-else-if="detail">
            <h4>{{ detail.reference.title || t('evidence.result') }}</h4>
            <p v-if="isUnverified" class="message-evidence__review-warning" role="status">{{ t('evidence.unverifiedReview') }}</p>
            <p v-if="detail.status !== 'snapshot'" role="status">{{ unavailableCopy }}</p>
            <details v-if="detail.status !== 'snapshot' && admin && detail.message"><summary>{{ t('evidence.adminDetails', 'Diagnostic details') }}</summary><pre>{{ detail.message }}</pre></details>
            <template v-if="detail.status === 'snapshot'">
              <p class="message-evidence__note">{{ t('evidence.snapshot') }}</p>
              <p v-if="isCalculation">{{ t('evidence.calculated') }}</p>
              <div v-if="columns.length" class="message-evidence__table">
                <table><thead><tr><th v-for="(column, index) in columns" :key="index">{{ column }}<small v-if="columnInfo[index]?.unit">{{ unitText(columnInfo[index].unit) }}</small></th></tr></thead><tbody><tr v-for="(row, index) in rows" :key="index"><td v-for="(cell, col) in row" :key="col">{{ displayCell(cell) }}</td></tr></tbody></table>
              </div>
              <p v-if="globalUnit">{{ t('evidence.unit') }}：{{ unitText(globalUnit) }}</p>
              <details v-if="unitDeclarations.length"><summary>{{ t('evidence.columnUnits') }}</summary><dl v-for="(declaration, index) in unitDeclarations" :key="index"><dt>{{ declaration.label }} · {{ unitText(declaration.unit) }}</dt><dd v-if="unitLevel(declaration.unit)">{{ t('evidence.unitLevel') }}：{{ unitLevel(declaration.unit) }}</dd><dd v-if="declaration.unit.expression">{{ t('evidence.unitExpression') }}：{{ declaration.unit.expression }}</dd></dl></details>
              <p v-if="cellPosition" class="message-evidence__note">{{ cellPosition }}</p>
              <details v-if="hasComputation && computation">
                <summary>{{ t('evidence.calculation') }}</summary>
                <pre v-if="computation.expression">{{ computation.expression }}</pre>
                <p>{{ t('evidence.inputScope') }}</p>
                <dl>
                  <template v-if="scopeText"><dt>{{ t('evidence.inputScopeLabel') }}</dt><dd>{{ scopeText }}</dd></template>
                  <template v-if="computation.input_count !== undefined"><dt>{{ t('evidence.inputCountLabel') }}</dt><dd>{{ computation.input_count }}</dd></template>
                </dl>
                <p v-if="computation.source_truncated || computation.input_complete === false">{{ t('evidence.partialSource') }}</p>
                <div v-if="chartAxes.length > 1">
                  <h5>{{ t('evidence.mappingLabel') }}</h5>
                  <dl>
                    <dt>{{ t('evidence.axisX', 'Horizontal axis') }}</dt><dd>{{ chartAxes[0] }}</dd>
                    <dt>{{ t('evidence.axisY', 'Vertical axis') }}</dt><dd>{{ chartAxes.slice(1).join(' · ') }}</dd>
                    <dt>{{ t('evidence.cellSource', 'Source') }}</dt><dd>{{ sourceLabels.join(' / ') }}</dd>
                  </dl>
                </div>
                <details v-if="admin">
                  <summary>{{ t('evidence.adminDetails', 'Diagnostic details') }}</summary>
                  <pre v-if="computation.sql">{{ computation.sql }}</pre>
                  <pre v-if="computation.coverage !== undefined">{{ JSON.stringify(computation.coverage, null, 2) }}</pre>
                  <pre v-if="computation.mappings?.length">{{ JSON.stringify(computation.mappings, null, 2) }}</pre>
                </details>
              </details>
              <div class="message-evidence__sources"><button v-for="(item, index) in detail.sources" :key="`${item.knowledge_id}:${index}`" type="button" :disabled="previewBusy" @click="openOriginal(index)">{{ t('evidence.openSource') }} · {{ sourceLabels[index] }}</button></div>
              <p v-if="previewBusy" role="status">{{ t('evidence.loading') }}</p>
              <p v-if="previewError" role="alert">{{ previewError }}</p>
              <p v-if="position" class="message-evidence__note">{{ position }}</p>
              <DocumentPreview v-if="original && source" :key="`${selected}:${sourceIndex}:${token}`" :source-blob="original" :file-type="source.file_name.split('.').pop() || ''" :file-name="source.file_name" active source-mode :locate="locate" class="message-evidence__preview" @located="onLocated" />
            </template>
          </template>
        </section>
      </div>
    </Teleport>
  </div>
</template>

<style scoped>
.message-evidence { margin-top: 10px; }
.message-evidence button { font: inherit; cursor: pointer; border: 1px solid var(--td-component-border); border-radius: var(--app-radius-sm); background: var(--td-bg-color-container); color: var(--td-text-color-primary); padding: 6px 10px; }
.message-evidence__open { color: var(--td-brand-color) !important; }
.message-evidence__backdrop { position: fixed; inset: 0; z-index: 2500; background: #0005; display: flex; justify-content: flex-end; }
.message-evidence__panel { width: min(760px, 100vw); height: 100dvh; overflow: auto; background: var(--td-bg-color-container); color: var(--td-text-color-primary); padding: 20px; box-sizing: border-box; }
.message-evidence__panel header { display: flex; align-items: center; justify-content: space-between; gap: 16px; }
.message-evidence__panel header h3 { margin: 0 0 12px; }
.message-evidence__panel button { font: inherit; cursor: pointer; border: 1px solid var(--td-component-border); border-radius: var(--app-radius-sm); background: var(--td-bg-color-container); color: var(--td-text-color-primary); padding: 6px 10px; }
.message-evidence__tabs, .message-evidence__sources { display: flex; gap: 8px; flex-wrap: wrap; margin: 12px 0; }
.message-evidence__tabs button { max-width: 100%; white-space: normal; overflow-wrap: anywhere; }
.message-evidence__tabs [aria-pressed=true] { border-color: var(--td-brand-color); color: var(--td-brand-color); }
.message-evidence__note { color: var(--td-text-color-secondary); font-size: var(--app-text-sm); }
.message-evidence__review-warning { color: var(--td-warning-color-active); }
.message-evidence__table { overflow: auto; max-height: 35vh; }
.message-evidence__table table { border-collapse: collapse; min-width: 100%; }
.message-evidence__table th, .message-evidence__table td { border: 1px solid var(--td-component-border); padding: 7px 10px; text-align: left; white-space: nowrap; }
.message-evidence__table small { display: block; font-weight: normal; color: var(--td-text-color-secondary); }
.message-evidence__panel dt { font-weight: 600; margin-top: 8px; }
.message-evidence__panel dd { margin-left: 12px; overflow-wrap: anywhere; }
.message-evidence__panel pre { white-space: pre-wrap; overflow-wrap: anywhere; font-size: var(--app-text-sm); }
.message-evidence__preview { height: min(70vh, 780px); min-height: 280px; border: 1px solid var(--td-component-border); }
@media (max-width: 500px) { .message-evidence__panel { padding: 14px; } .message-evidence__preview { height: 65vh; } }
</style>
