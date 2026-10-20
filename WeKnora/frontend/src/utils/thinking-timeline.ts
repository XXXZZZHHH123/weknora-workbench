import type { ComposerTranslation } from 'vue-i18n'
import { getRagPipelineStepTitle } from './agent-tool-display.ts'
import { RAG_RETRIEVAL_TOOL_NAMES, RAG_TIMELINE_TOOL_NAMES } from './rag-pipeline-history.ts'

/**
 * Display helpers for the collapsible "thought for Xs" timeline of quick-answer
 * turns. Every real tool step is shown with a readable title; runtime-internal
 * steps that carry no user meaning are hidden. Titles depend only on the tool
 * protocol (tool name / operation), never on the question.
 */

const HIDDEN_TOOL_NAMES = new Set([
  'thinking',
  'expand_task_tools',
  'todo_write',
  'final_answer',
  'plan_task',
])

const HIDDEN_STRUCTURED_OPERATIONS = new Set(['describe_tools'])

const STRUCTURED_OPERATION_KEYS: Record<string, string> = {
  list_datasets: 'listDatasets',
  list_series: 'listSeries',
  list_charts: 'listCharts',
  query_series: 'querySeries',
  execute_field_plan: 'querySeries',
  analyze_table: 'analyzeTable',
  inspect_source: 'inspectSource',
  compare_series: 'compareSeries',
  convert_price: 'convertPrice',
  resolve_fields: 'resolveFields',
  confirm_fields: 'resolveFields',
  get_result_evidence: 'getResultEvidence',
}

const TOOL_KEYS: Record<string, string> = {
  read_document: 'readDocument',
  list_documents: 'listDocuments',
  bind_document_scope: 'bindDocumentScope',
  wiki_search: 'wikiSearch',
  wiki_read_page: 'wikiReadPage',
  analyze_document_tables: 'analyzeDocumentTables',
  calculate_document_table: 'calculate',
  calculate_source_values: 'calculate',
  web_search: 'webSearch',
  web_fetch: 'webFetch',
}

type TimelineEvent = {
  tool_name?: unknown
  pending?: unknown
  success?: unknown
  arguments?: unknown
  tool_data?: unknown
}

function asRecord(value: unknown): Record<string, unknown> | null {
  if (value && typeof value === 'object' && !Array.isArray(value)) return value as Record<string, unknown>
  if (typeof value === 'string') {
    try {
      const parsed = JSON.parse(value)
      if (parsed && typeof parsed === 'object' && !Array.isArray(parsed)) return parsed as Record<string, unknown>
    } catch {
      return null
    }
  }
  return null
}

/** structured_data carries its operation in arguments; split tools carry it in the name. */
export function structuredOperation(toolName: string, args: unknown): string {
  if (toolName === 'structured_data') {
    const operation = asRecord(args)?.operation
    return typeof operation === 'string' ? operation : ''
  }
  if (toolName.startsWith('structured_')) return toolName.slice('structured_'.length)
  return ''
}

export function isThinkingTimelineStep(toolName: unknown, args: unknown): boolean {
  if (typeof toolName !== 'string' || !toolName || HIDDEN_TOOL_NAMES.has(toolName)) return false
  return !HIDDEN_STRUCTURED_OPERATIONS.has(structuredOperation(toolName, args))
}

function documentTitle(toolData: unknown): string {
  const document = asRecord(asRecord(toolData)?.document)
  const title = document?.title ?? document?.file_name
  return typeof title === 'string' ? title.trim() : ''
}

export function getThinkingTimelineStepTitle(t: ComposerTranslation, event: TimelineEvent): string {
  const toolName = typeof event.tool_name === 'string' ? event.tool_name : ''
  if (RAG_TIMELINE_TOOL_NAMES.has(toolName) || RAG_RETRIEVAL_TOOL_NAMES.has(toolName)) {
    const title = getRagPipelineStepTitle(t, {
      tool_name: toolName,
      pending: event.pending === true,
      success: event.success as boolean | undefined,
      arguments: event.arguments,
      tool_data: event.tool_data as Record<string, unknown> | undefined,
    })
    if (title) return title
  }
  const operation = structuredOperation(toolName, event.arguments)
  if (operation || toolName === 'structured_data') {
    return t(`agentStream.thinkingTimeline.steps.${STRUCTURED_OPERATION_KEYS[operation] || 'structuredFallback'}`)
  }
  if (toolName === 'read_document') {
    const title = documentTitle(event.tool_data)
    if (title) return t('agentStream.thinkingTimeline.steps.readDocumentNamed', { title })
  }
  const key = TOOL_KEYS[toolName]
  return t(`agentStream.thinkingTimeline.steps.${key || 'generic'}`)
}

/** "Thought for 12s" / "Thought for 2m 5s"; empty when the duration is unknown. */
export function formatThoughtDuration(t: ComposerTranslation, durationMs: unknown): string {
  const ms = Number(durationMs)
  if (!Number.isFinite(ms) || ms <= 0) return ''
  const totalSeconds = Math.max(1, Math.round(ms / 1000))
  if (totalSeconds < 60) return t('agentStream.thinkingTimeline.thoughtForSeconds', { seconds: totalSeconds })
  return t('agentStream.thinkingTimeline.thoughtForMinutes', {
    minutes: Math.floor(totalSeconds / 60),
    seconds: totalSeconds % 60,
  })
}
