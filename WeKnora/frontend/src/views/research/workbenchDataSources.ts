import type { CentralDataset, ResearchWorkbench, ResearchWorkbenchSource, WorkbenchSourceInput } from '@/api/workspaces'

export const MAX_WORKBENCH_SOURCES = 8
export const sourceKey = (source: WorkbenchSourceInput) => `${source.source_type}:${source.source_id}`

export function validateSourceSelection(selected: WorkbenchSourceInput[], available: WorkbenchSourceInput[]): string {
  if (selected.length > MAX_WORKBENCH_SOURCES) return `最多选择 ${MAX_WORKBENCH_SOURCES} 个数据来源。`
  const allowed = new Set(available.map(sourceKey)), keys = selected.map(sourceKey)
  if (new Set(keys).size !== keys.length) return '数据来源重复，请重新选择。'
  return selected.some((source, index) => !source.source_id || !allowed.has(keys[index]!)) ? '部分来源不可用，请刷新后重新选择。' : ''
}

export function workbenchSourceLabel(workbench: ResearchWorkbench, libraries: Array<{ id: string; name: string }>): string {
  if (workbench.scope_version === 2) {
    const active = (workbench.sources || []).filter(source => source.status === 'active')
    const unavailable = (workbench.sources || []).filter(source => source.status === 'unavailable').length
    const labels = active.map(source => source.name || '数据来源')
    if (unavailable) labels.push(`${unavailable} 个来源不可用`)
    return labels.join('、') || '未关联来源'
  }
  return libraries.find(library => library.id === workbench.knowledge_base_id)?.name || workbench.knowledge_base_name || '知识库'
}

export function directKnowledgeBaseIds(workbench: ResearchWorkbench): string[] {
  if (workbench.scope_version !== 2) return workbench.can_read_source !== false && workbench.knowledge_base_id ? [workbench.knowledge_base_id] : []
  return [...new Set((workbench.sources || []).filter(source => source.status === 'active' && source.can_read && !source.delegated && source.knowledge_base_id).map(source => source.knowledge_base_id!))]
}

export function sourceOptions(libraries: Array<{ id: string; name: string }>, central: CentralDataset[], existing: ResearchWorkbenchSource[] = []) {
  const backing = new Set(central.map(source => source.knowledge_base_id))
  const result = [
    ...central.map(source => ({ source_type: 'central_dataset' as const, source_id: source.id, name: source.name, hint: '中心库', disabled: false })),
    ...libraries.filter(library => !backing.has(library.id)).map(library => ({ source_type: 'knowledge_base' as const, source_id: library.id, name: library.name, hint: '知识库', disabled: false })),
  ]
  const keys = new Set(result.map(sourceKey))
  for (const source of existing) {
    if (!keys.has(sourceKey(source)) && ['active', 'unavailable'].includes(source.status)) result.push({ source_type: source.source_type, source_id: source.source_id, name: source.name || '已关联来源', hint: '当前无直接访问权限', disabled: true })
  }
  return result
}
