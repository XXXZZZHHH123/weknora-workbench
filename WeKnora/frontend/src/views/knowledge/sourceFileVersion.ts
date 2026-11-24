import type { ListKnowledgeFilesParams } from '@/api/knowledge-base'
import type { UploadPayload } from '@/stores/uploadQueue'
import { buildUploadFileName } from './folderTree'

export interface ExistingSourceFile {
  id: string
  file_name: string
  folder_path?: string
  created_at?: string
}

export interface SourceFileVersionChoice {
  index: number
  fileName: string
  candidates: ExistingSourceFile[]
  knowledgeId: string
}

// Bound both the number of file_names values and their percent-encoded query size.
const MAX_QUERY_NAMES = 40
const MAX_QUERY_BYTES = 4000
const LOOKUP_PAGE_SIZE = 100

export async function findSourceFileVersionChoices(
  files: File[],
  targetFolder: string,
  list: (params: ListKnowledgeFilesParams) => Promise<{
    success?: boolean
    data?: ExistingSourceFile[]
    total?: number
  }>,
): Promise<SourceFileVersionChoice[]> {
  const names = [...new Set(files.map(file => file.name.trim()))]
  const existing = new Map<string, ExistingSourceFile[]>()
  let offset = 0
  while (offset < names.length) {
    const batch: string[] = []
    let bytes = 0
    while (offset < names.length && batch.length < MAX_QUERY_NAMES) {
      const name = names[offset]
      const size = new URLSearchParams({ file_names: name }).toString().length + 1
      if (batch.length && bytes + size > MAX_QUERY_BYTES) break
      batch.push(name)
      bytes += size
      offset++
    }
    for (let page = 1; ; page++) {
      const result = await list({ file_names: batch, page, page_size: LOOKUP_PAGE_SIZE })
      if (!result?.success || !Array.isArray(result.data) || typeof result.total !== 'number') {
        throw new Error('Could not list existing files')
      }
      for (const row of result.data) {
        if (!row.id) continue
        const rows = existing.get(row.file_name)
        if (!rows) existing.set(row.file_name, [row])
        else if (!rows.some(candidate => candidate.id === row.id)) rows.push(row)
      }
      if (!result.data.length || page * LOOKUP_PAGE_SIZE >= result.total) break
    }
  }

  const choices: SourceFileVersionChoice[] = []
  const selectedIds = new Set<string>()
  for (const [index, file] of files.entries()) {
    const rows = existing.get(file.name.trim())
    if (!rows?.length) continue
    const filePath = buildUploadFileName(file, targetFolder)
    const folder = filePath ? filePath.slice(0, filePath.lastIndexOf('/')) : ''
    const candidates = [...rows].sort((a, b) =>
      Number((b.folder_path || '') === folder) - Number((a.folder_path || '') === folder))
    const knowledgeId = (candidates.find(row => !selectedIds.has(row.id)) || candidates[0]).id
    choices.push({ index, fileName: file.name, candidates, knowledgeId })
    selectedIds.add(knowledgeId)
  }
  return choices
}

export function buildSourceFileUploads(
  files: File[], targetFolder: string, choices: ReadonlyMap<number, string> | undefined,
): UploadPayload[] {
  return files.map((file, index) => {
    const replaceKnowledgeId = choices?.get(index)
    if (replaceKnowledgeId) return { file, replaceKnowledgeId }
    return {
      file,
      fileName: buildUploadFileName(file, targetFolder),
      ...(replaceKnowledgeId === '' ? { saveAsNew: true } : {}),
    }
  })
}

export function sourceFileVersionConflict(choices: SourceFileVersionChoice[]) {
  const targets = choices.map(choice => choice.knowledgeId).filter(Boolean)
  return new Set(targets).size !== targets.length
}
