/** File-format routing for stored originals; unrelated to question intent. */
export interface ExcelSourceFile {
  id?: string
  type?: string
  file_type?: string
  file_name?: string
  original_file_name?: string
  knowledge_base_id?: string
}

export function isExcelFilename(name: string): boolean {
  if (name.lastIndexOf('.') < 0) return false
  const extension = name.slice(name.lastIndexOf('.') + 1).toLowerCase()
  return extension === 'xlsx' || extension === 'xls'
}

export function excelSourceExtension(file: ExcelSourceFile): string {
  if (file.type && file.type !== 'file') return ''
  const name = file.original_file_name || file.file_name || ''
  const extension = (file.file_type || (name.includes('.') ? name.slice(name.lastIndexOf('.') + 1) : '')).replace(/^\./, '').toLowerCase()
  return extension === 'xlsx' || extension === 'xls' ? extension : ''
}

export function isExcelSource(file: ExcelSourceFile): boolean {
  return !!excelSourceExtension(file)
}

export function isPdfSource(file: ExcelSourceFile): boolean {
  if (file.type && file.type !== 'file') return false
  const name = file.original_file_name || file.file_name || ''
  return (file.file_type || (name.includes('.') ? name.slice(name.lastIndexOf('.') + 1) : '')).replace(/^\./, '').toLowerCase() === 'pdf'
}

export function isStructuredSource(file: ExcelSourceFile): boolean {
  return isExcelSource(file) || isPdfSource(file)
}

export function structuredSourceLocation(kbId: string, file: ExcelSourceFile) {
  if (!kbId || !file.id || (excelSourceExtension(file) !== 'xlsx' && !isPdfSource(file))
      || (file.knowledge_base_id && file.knowledge_base_id !== kbId)) return null
  return { path: `/platform/knowledge-bases/${kbId}/structured-data`,
    query: { mode: 'import', source_knowledge_id: file.id, ...(isPdfSource(file) ? { source_file_type: 'pdf' } : {}) } }
}

export interface NativeUploadScope { userId: string; tenantId: string; knowledgeBaseId: string }

export function sameNativeUploadScope(expected: NativeUploadScope, current: NativeUploadScope): boolean {
  return !!expected.userId && !!expected.tenantId && !!expected.knowledgeBaseId
    && expected.userId === current.userId && expected.tenantId === current.tenantId
    && expected.knowledgeBaseId === current.knowledgeBaseId
}
