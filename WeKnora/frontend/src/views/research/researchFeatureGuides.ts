import type { SpotlightGuideStep } from '@/types/spotlightGuide'

export const researchGuidePages = ['home', 'market', 'indicators', 'judgment', 'diagram'] as const
export type ResearchGuidePage = typeof researchGuidePages[number]
export type ResearchGuideOutcome = 'pending' | 'completed' | 'skipped'
export interface ResearchGuideScope { tenantId: string; userId: string; workbenchId: string; knowledgeBaseId: string }
export interface ResearchGuideLedger { version: 1; pages: Record<ResearchGuidePage, ResearchGuideOutcome> }
type Storage = Pick<globalThis.Storage, 'getItem' | 'setItem'>

export function researchGuideKey(scope: ResearchGuideScope): string {
  const values = [scope.tenantId, scope.userId, scope.workbenchId, scope.knowledgeBaseId]
  return values.every(value => typeof value === 'string' && value.trim())
    ? 'weknora:research-feature-guide:v1:' + values.map(encodeURIComponent).join(':') : ''
}
export function readResearchGuide(storage: Storage, scope: ResearchGuideScope): ResearchGuideLedger | null {
  const key = researchGuideKey(scope)
  if (!key) return null
  try {
    const value = JSON.parse(storage.getItem(key) || 'null')
    return value?.version === 1 && researchGuidePages.every(page => ['pending', 'completed', 'skipped'].includes(value.pages?.[page])) ? value : null
  } catch { return null }
}
// Eligibility is recorded only by the successful creation flow. Missing or old
// state never enrolls an existing workbench into a new set of popups.
export function registerResearchGuide(storage: Storage, scope: ResearchGuideScope, mode: string): boolean {
  const key = researchGuideKey(scope)
  if (!key || mode !== 'isolated') return false
  try {
    if (storage.getItem(key) !== null) return false
    storage.setItem(key, JSON.stringify({ version: 1, pages: Object.fromEntries(researchGuidePages.map(page => [page, 'pending'])) }))
    return true
  } catch { return false }
}
export function finishResearchGuide(storage: Storage, scope: ResearchGuideScope, page: ResearchGuidePage, outcome: Exclude<ResearchGuideOutcome, 'pending'>): boolean {
  const ledger = readResearchGuide(storage, scope)
  if (!ledger || !researchGuidePages.includes(page) || !['completed','skipped'].includes(outcome)) return false
  try {
    storage.setItem(researchGuideKey(scope), JSON.stringify({ ...ledger, pages: { ...ledger.pages, [page]: outcome } }))
    return true
  } catch { return false }
}
export function researchGuideFramePage(scope: ResearchGuideScope, frame: { origin: string; pathname: string; context: unknown }, origin: string): ResearchGuidePage | null {
  if (!researchGuideKey(scope) || frame.origin !== origin) return null
  const context = frame.context as Record<string, unknown> | null
  if (!context || String(context.tenant) !== scope.tenantId || context.user !== scope.userId
      || context.workbench !== scope.workbenchId || context.knowledge_base !== scope.knowledgeBaseId) return null
  const prefix = `/research/workbenches/${encodeURIComponent(scope.workbenchId)}`
  const paths: Record<ResearchGuidePage, string> = { home: '/', market: '/sn', indicators: '/sn/indicators', judgment: '/sn/judgment', diagram: '/sn/diagram' }
  const pathname = frame.pathname.endsWith('/') ? frame.pathname.slice(0, -1) : frame.pathname
  return researchGuidePages.find(page => pathname === prefix + (paths[page] === '/' ? '' : paths[page])) ?? null
}

export function researchFeatureSteps(page: ResearchGuidePage, open: (panel: 'market' | 'logic' | 'logic-analysis' | 'logic-close' | 'diagram' | 'diagram-close') => void | Promise<void>): SpotlightGuideStep[] {
  const step = (key: string, target: string, title: string, description: string, before?: () => void | Promise<void>): SpotlightGuideStep => ({ key, target, title, description, placement: 'bottom', ...(before ? { before } : {}) })
  if (page === 'home') return [
    step('start', '.research-nav-links .nav-btn[href*="/sn"]', '先建立自己的研究视角', '在市场总览选择并排列核心指标；到指标页搜索知识库数据、加入自选。资料尚未确认时先去知识库完成结构化导入，工作台不会借用其他工作台的数据。'),
    step('modules', '.research-nav-links', '每个研究功能都有首次提示', '研究判断用于导入逻辑、分析成草稿并核对区间；产业图用于导入结构、绑定指标和编辑布局。首次进入这些页面会分别提示，跳过只关闭当前页面的引导。'),
  ]
  if (page === 'market') return [
    step('core', '#market-core-open', '选择自己的核心指标', '点击“选择与排序”，从当前知识库指标中挑选最多 12 项；核心指标会在这里形成市场总览。'),
    step('search', '#market-core-search', '从真实指标目录选择', '按名称、来源或单位搜索并勾选需要的指标。目录为空时先完成知识库结构化解析，不会生成示例行情。', () => open('market')),
    step('order', '#market-core-selected, #market-core-selected-empty', '调整位置，自动加入自选', '使用上移、下移调整显示顺序，点击保存后选中指标自动加入指标页自选；从核心移出不会删除自选。取消会保留原有选择。本引导不会替你保存。'),
  ]
  if (page === 'indicators') return [
    step('search', '#indicator-search', '全局搜索知识库指标', '搜索名称、代码、来源或口径，先核对单位、数据时点和来源。当前知识库指标集中在这里；已配置的 Default 宏观指标也会在这里显示。'),
    step('bookmark', '.indicator-bookmark, #indicator-no-results, #indicator-favorites-empty', '把常用指标加入自选', '点击指标名旁的书签加入或移出自选。若当前没有可选指标，先在知识库完成数据确认，再返回这里搜索。'),
    step('favorites', '#indicator-favorites-title', '集中查看自选指标', '我的自选集中显示最新值与走势，市场总览选中的核心指标也会加入这里。核心排列和自选按当前用户、空间与工作台分别保存。'),
  ]
  if (page === 'judgment') return [
    step('manage', '[data-open-logic]', '在这里管理研究逻辑', '查看当前 Markdown、复用当前逻辑或原文，导入新版本时保留历史。先分析、再核对，不会直接覆盖当前研究判断。'),
    step('input', '#logic-input', '导入文件，或按结构填写原文', '选择 .md / .txt，或粘贴研究逻辑。可用通用骨架说明主矛盾、证据与指标口径、边际变化、区间或阈值及证伪条件；也可复用当前版本后修改。', () => open('logic-analysis')),
    step('generate', '#logic-model, #logic-generate', '选择模型生成待确认草稿', '点击“分析原文”才会调用模型，搜索并绑定知识库真实指标。核对绑定、未解决事项和版本差异后，手动确认保存新草稿；生成和保存都不代表已生效，引导不会替你调用模型。'),
    step('ranges', '#judgment-thresholds, #judgment-empty', '怎样判断是否落入区间', '将关联指标的实际值与写明的区间或触发条件比较，同时核对单位、数据时点及来源。“在区间内”“已触发”“数据缺失”是不同状态；缺失数据不能当作未触发。草稿需补齐阻断项，不能把模型文字直接当成已核验判断。', () => open('logic-close')),
  ]
  return [
    step('markdown', '#dg-md-btn', '从 md 管理导入产业结构', '在 md 管理查看并编辑当前结构，粘贴已有 Markdown，或选择资料交给已配置模型生成。生成内容需要核对后再导入为新布局或应用到当前布局。'),
    step('generate', '#dg-generator, #dg-md-text', '生成结构，并核对指标绑定', '可选择知识库原件、添加图片或补充要求，再手动点击生成配置。检查供给、需求、库存等关系及绑定指标；未找到真实指标的节点会保留未绑定，不会编造数值。本引导不调用模型。', () => open('diagram')),
    step('edit', '#dg-edit-btn', '编辑画布的布局与节点', '关闭 md 管理后进入编辑布局，可增减节点和分组、调整位置与连线；选中节点核对并绑定真实指标，保存后保留修改。导入新结构可能重新排布，先保留需要的版本。', () => open('diagram-close')),
    step('evidence', '#dg-wrap', '让结构随数据变化', '节点显示绑定指标的最新值、变化和数据状态。核对单位、时点与来源，区分代理指标、缺失和断更；通过节点详情与趋势对比继续研究。没有数据的节点不会显示为已验证供需结论。'),
  ]
}
