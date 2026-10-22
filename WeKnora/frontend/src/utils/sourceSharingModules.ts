export const SOURCE_SHARING_MODULES = [
  { key: 'home', label: '工作台', hint: '仅展示导航概览，不加载指标汇总' },
  { key: 'market', label: '市场总览', hint: '仅展示本台选择的核心指标' },
  { key: 'indicators', label: '指标', hint: '仅本台绑定指标，无全库查询或下载' },
  { key: 'judgments', label: '研究判断', hint: '仅本人版本概览，不展示正文或区间计算' },
  { key: 'strategies', label: '策略', hint: '仅本人策略条目，不展示正文或触发计算' },
  { key: 'diagram', label: '产业图', hint: '展示本台直接绑定的必要指标数值' },
  { key: 'reports', label: '日报', hint: '仅本人日报记录，不展示摘要或证据快照' },
] as const

export type SourceSharingModule = typeof SOURCE_SHARING_MODULES[number]['key']

export function sourceSharingModulesDraft(value: unknown): SourceSharingModule[] {
  if (!Array.isArray(value)) return []
  return SOURCE_SHARING_MODULES.map(item => item.key).filter(key => value.includes(key))
}
