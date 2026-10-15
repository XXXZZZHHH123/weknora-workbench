export type WorkbenchTemplateId = 'industrial' | 'data-tracking' | 'topic' | 'blank'

export const workbenchTemplates = [
  { id: 'industrial', name: '产业研究', description: '从资料建立产业结构、指标、研究判断和简报。', modules: ['data', 'structure', 'judgment', 'monitoring', 'brief', 'strategies', 'export'] },
  { id: 'data-tracking', name: '数据跟踪', description: '跟踪已确认的指标，核对趋势、对比与更新状态。', modules: ['data', 'trends', 'compare', 'freshness', 'export'] },
  { id: 'topic', name: '专题研究', description: '围绕一个问题组织资料、笔记与证据，逐步形成简报。', modules: ['sources', 'notes', 'evidence', 'assistant', 'brief'] },
  { id: 'blank', name: '空白工作台', description: '只建立独立的资料范围，需要时再补充研究配置。', modules: ['sources', 'assistant'] },
] as const

export const workbenchModuleNames: Record<string, string> = {
  data: '指标与图表', structure: '研究结构', judgment: '研究判断', monitoring: '跟踪事项',
  brief: '简报', strategies: '策略', export: '导出', trends: '趋势', compare: '对比',
  freshness: '更新状态', sources: '资料与来源', notes: '研究笔记', evidence: '研究证据', assistant: '问答助手',
}

export function templateById(id: string) {
  return workbenchTemplates.find(template => template.id === id) || workbenchTemplates[3]
}

export function isolatedWorkbenchScope(workbench: { id: string; knowledge_base_id: string; workspace_mode?: string }, expectedId: string, expectedKb: string): boolean {
  return workbench.workspace_mode === 'isolated' && workbench.id === expectedId && workbench.knowledge_base_id === expectedKb
}
