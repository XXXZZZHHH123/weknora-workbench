export type StructuredSourceContext = {
  knowledge_base_id: string
  knowledge_base_tenant_id?: number | string
}

export type StructuredAnswerSource = {
  kind: 'excel' | 'pdf'
  data: any
  context: StructuredSourceContext | null
  operation: string
}

const TOOL_SUFFIX = /^(?:structured_)?(query_series|analyze_table|query_pdf_table|read_pdf_table|list_pdf_tables)(?:_[a-z0-9]+)?$/
const STRUCTURED_OPERATION = /^(?:query_series|analyze_table|query_pdf_table|read_pdf_table|list_pdf_tables)$/
const HEX_ID = /^[a-f0-9]{24}$/i
const PDF_TABLE_ID = /^pdft_[A-Za-z0-9_.-]{1,155}$/
const HASH = /^[a-f0-9]{64}$/i

function parseResult(value: unknown): any[] {
  if (!value || typeof value !== 'object') return []
  const outer = value as any
  return [outer.data, outer].filter((candidate: any) =>
    candidate && typeof candidate === 'object' && !Array.isArray(candidate))
}

/** Only successful structured tool results can create a source card. */
export function structuredAnswerSources(events: any[], completed = true): StructuredAnswerSource[] {
  if (!completed || !Array.isArray(events)) return []
  const found = new Map<string, StructuredAnswerSource>()
  for (const event of events) {
    const toolName = String(event?.tool_name || '')
    const rawOperation = String(event?.operation || event?.arguments?.operation || toolName)
    const withoutMcpPrefix = rawOperation.replace(/^mcp__/, '')
    const nativeMatch = TOOL_SUFFIX.exec(withoutMcpPrefix)
    const operation = STRUCTURED_OPERATION.test(rawOperation) ? rawOperation : nativeMatch?.[1] || rawOperation
    const supported = rawOperation === 'structured_data' || STRUCTURED_OPERATION.test(operation) || TOOL_SUFFIX.test(withoutMcpPrefix)
    if (event?.type !== 'tool_call' || !supported || event.pending || event.success !== true) continue
    for (const parsed of parseResult(event.tool_data)) {
      const results = parsed.kind === 'structured_pdf_tables' && Array.isArray(parsed.tables)
        ? parsed.tables.filter((table: any) => table && typeof table === 'object').map((table: any) => ({ ...table, kind: 'structured_pdf_tables', source_kind: 'pdf_table', result_status: 'ok', source_context: parsed.source_context }))
        : [parsed]
      for (const data of results) {
      if ((data.result_status || data.status) !== 'ok') continue
      const source = data.source && typeof data.source === 'object' ? data.source : {}
      const kind = data.source_kind === 'pdf_table' || data.kind === 'structured_pdf_tables' ||
        data.table_id || (data.kind === 'structured_source_inspection' && data.source_kind === 'pdf_table') ? 'pdf' : 'excel'
      const datasetId = data.dataset_id
      const tableId = data.table_id
      const context = data.source_context && typeof data.source_context.knowledge_base_id === 'string' && data.source_context.knowledge_base_id.trim()
        ? { knowledge_base_id: data.source_context.knowledge_base_id,
            ...(data.source_context.knowledge_base_tenant_id != null ? { knowledge_base_tenant_id: data.source_context.knowledge_base_tenant_id } : {}) }
        : null
      if (kind === 'excel' && (!HEX_ID.test(String(datasetId || '')) || !HASH.test(String(source.sha256 || '')))) continue
      if (kind === 'pdf' && (!PDF_TABLE_ID.test(String(tableId || '')) || !HASH.test(String(source.sha256 || '')))) continue
      const meaningful = kind === 'excel'
        ? data.kind === 'structured_analysis' || operation === 'query_series'
        : ['structured_analysis', 'structured_source_inspection', 'structured_pdf_tables'].includes(data.kind)
      if (!meaningful) continue
      const key = JSON.stringify([kind, datasetId || tableId, operation,
        data.series?.series_id || data.table_id || '', context?.knowledge_base_id || '',
        context?.knowledge_base_tenant_id == null ? '' : String(context.knowledge_base_tenant_id)])
      found.set(key, { kind, data, context, operation })
      }
    }
  }
  return [...found.values()]
}

export function parseExcelCell(address: unknown): { row: number; column: number; address: string } | null {
  if (typeof address !== 'string') return null
  const match = /^\$?([A-Z]{1,3})\$?([1-9]\d*)$/i.exec(address)
  if (!match) return null
  let column = 0
  for (const char of match[1].toUpperCase()) column = column * 26 + char.charCodeAt(0) - 64
  const row = Number(match[2])
  if (!Number.isSafeInteger(row) || row > 1_048_576 || column > 16_384) return null
  return { row, column, address: `${match[1].toUpperCase()}${row}` }
}

export function isStructuredImportId(value: unknown): value is string {
  return typeof value === 'string' && HEX_ID.test(value)
}
