import { MAX_FOLDER_DEPTH, MAX_FOLDER_PATH_LENGTH, MAX_FOLDER_SEGMENT_LENGTH, normalizeFolderPath } from '../knowledge/folderTree'

export const KNOWLEDGE_DIRECTORY_EVENT = 'weknora:knowledge-directory-change'
export const KNOWLEDGE_DIRECTORY_PAGE_SIZE = 50

export interface KnowledgeDirectoryFile {
  id: string
  knowledge_base_id: string
  folder_path: string
  file_name?: string
  title?: string
  file_type?: string
  type?: string
}

export interface KnowledgeDirectoryFilePage {
  files: KnowledgeDirectoryFile[]
  page: number
  total: number
  loading: boolean
  error: string
}

export function validateKnowledgeDirectoryPage(response: any, kbId: string, path: string): { files: KnowledgeDirectoryFile[]; total: number } {
  if (!response || !Array.isArray(response.data) || !Number.isSafeInteger(response.total) || response.total < 0) {
    throw new Error('目录文件响应无效，请重试。')
  }
  if (response.data.length > KNOWLEDGE_DIRECTORY_PAGE_SIZE || response.data.some((file: any) => !file || typeof file.id !== 'string' || !file.id
      || file.knowledge_base_id !== kbId || file.folder_path !== path)) {
    throw new Error('目录文件范围不一致，请刷新目录后重试。')
  }
  return { files: response.data, total: response.total }
}

export function knowledgeDirectoryFileLocation(kbId: string, file: KnowledgeDirectoryFile) {
  if (!kbId || file.knowledge_base_id !== kbId || !file.id || typeof file.folder_path !== 'string') return null
  return { path: `/platform/knowledge-bases/${encodeURIComponent(kbId)}`,
    query: { ...(file.folder_path ? { folder: file.folder_path } : {}), knowledge_id: file.id } }
}

export function knowledgeDirectorySelection(query: Record<string, unknown>) {
  const all = query.directory === 'all'
  const path = all || typeof query.folder !== 'string' ? '' : normalizeFolderPath(query.folder)
  return { all, path }
}

export function newKnowledgeFolderPath(parent: string, rawName: string, existingPaths: string[]): { path: string; error: string } {
  const name = rawName.trim()
  if (!name || name === '.' || name === '..' || /[\\/\u0000-\u001f\u007f]/.test(name) || name.endsWith('.')) return { path: '', error: '请填写有效文件夹名称，不能包含路径分隔符、控制字符或以点结尾。' }
  const path = parent ? `${parent}/${name}` : name
  const bytes = (value: string) => new TextEncoder().encode(value).length
  if (bytes(name) > MAX_FOLDER_SEGMENT_LENGTH || bytes(path) > MAX_FOLDER_PATH_LENGTH) return { path: '', error: '名称或目录路径过长，请缩短后重试。' }
  if (path.split('/').length > MAX_FOLDER_DEPTH) return { path: '', error: '目录层级已达到上限。' }
  if (existingPaths.includes(path)) return { path: '', error: '当前目录已有同名文件夹。' }
  return { path, error: '' }
}
