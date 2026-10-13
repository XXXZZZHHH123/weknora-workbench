export type AnswerStage = 'waiting' | 'sources' | 'data' | 'generation' | 'verification' | 'processing'
type StreamEvent = Record<string, unknown>

export function toolAnswerStage(event: StreamEvent): AnswerStage {
  const name = String(event.tool_name || '')
  const args = (event.arguments || {}) as Record<string, unknown>
  const operation = String(event.operation || (name === 'structured_data' ? args.operation || '' : name))
  if (/inspect_source|source_verification/.test(operation)) return 'verification'
  if (/query_series|analyze_table|analyze_dataset|query_database/.test(operation)) return 'data'
  if (/search_knowledge|read_document|search_web|web_search|web_fetch|list_datasets|list_series|resolve_fields|confirm_fields/.test(operation)) return 'sources'
  return 'processing'
}

/** Use actual backend phase events; pending tool work takes precedence. */
export function answerProgressStage(events: StreamEvent[] = []): AnswerStage {
  const pending = events.filter(event => event.type === 'tool_call' && event.pending === true)
  if (pending.length) return toolAnswerStage(pending[pending.length - 1])
  for (let i = events.length - 1; i >= 0; i--) {
    const event = events[i]
    if (event.type === 'progress' && ['sources', 'generation', 'verification'].includes(String(event.stage))) {
      return event.stage as AnswerStage
    }
    if (event.type === 'answer' && event.content && !event.superseded) return 'generation'
  }
  if (events.some(event => event.type === 'thinking')) return 'generation'
  return 'waiting'
}
