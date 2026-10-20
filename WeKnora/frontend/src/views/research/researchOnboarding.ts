import type { WorkbenchOverview } from '@/api/isolatedResearch'
import type { SpotlightGuideStep } from '@/types/spotlightGuide'

export function buildResearchOnboardingSteps(
  knowledgeBaseName: string,
  overview: WorkbenchOverview,
  context: 'home' | 'sources',
): SpotlightGuideStep[] {
  const counts = overview.counts
  const steps: SpotlightGuideStep[] = []
  if (context === 'home') steps.push({
    key: 'scope', target: '[data-guide="research-kb"]', placement: 'bottom',
    title: '研究范围已绑定',
    description: `本工作台使用「${knowledgeBaseName || '当前知识库'}」的资料。原件与结构化数据在知识库统一维护，可从这里返回。`,
  })
  if (!counts.sources) steps.push({
    key: 'sources', target: context === 'sources' ? '[data-guide="research-upload"]' : '[data-guide="research-sources"]', placement: 'bottom',
    title: '先准备研究资料',
    description: '添加文件或文件夹，保留原件与目录层级。Excel 先保存原件，再到知识库确认结构；其他文档按知识库配置解析。',
  })
  else if (counts.excel_pending > 0) steps.push({
    key: 'review', target: context === 'sources' ? '[data-guide="research-structured"], [data-guide="research-kb-manage"]' : '[data-guide="research-kb"]', placement: 'bottom',
    title: '继续确认表格数据',
    description: `已有 ${counts.sources} 份原件，${counts.excel_pending} 份表格仍待核对。确认工作表、字段与单位后，指标会在当前工作台使用；已确认的数据不必重导。`,
  })
  else if (!counts.indicators) steps.push({
    key: 'documents', target: context === 'sources' ? '[data-guide="research-source-preview"], [data-guide="research-kb-manage"]' : '[data-guide="research-sources"]', placement: 'bottom',
    title: '查看已有资料',
    description: `已有 ${counts.sources} 份原件，${counts.documents_ready} 份文档已解析。可以核对原文和处理状态；需要表格指标时，再到知识库进行结构化确认。`,
  })
  else steps.push({
    key: 'ready', target: context === 'sources' ? '[data-guide="research-kb-manage"]' : '[data-guide="research-sources"]', placement: 'bottom',
    title: '沿用已准备的数据',
    description: `当前已有 ${counts.indicators} 个指标、${counts.observations} 条观测，无需重新上传或导入。资料与来源入口可查看原件，指标和产业图使用知识库中的真实数据。`,
  })
  steps.push({
    key: 'research', target: '[data-guide="research-chat"]', placement: 'bottom',
    title: overview.readiness.tasks.some(task => task.id === 'research' && task.status === 'ready') ? '继续当前研究' : '开始研究与问答',
    description: '问答助手默认关联当前知识库，点击后在右侧常驻，收起即可继续研究。产业图、研究判断和日报可按需要逐步建立；看完提示不会替你确认资料或生成研究结果。',
  })
  return steps
}
