export const SOURCE_SHARING_MODULES = [
  { key: 'home', label: '工作台', hint: '只读展示主人共享的待办、简报摘要及本台绑定的必要数值' },
  { key: 'market', label: '市场总览', hint: '展示本台核心指标；同时授权研究判断时才展示研判摘要与阈值' },
  { key: 'indicators', label: '指标', hint: '仅本台绑定指标，无全库查询或下载' },
  { key: 'judgments', label: '研究判断', hint: '只读展示主人已分享的完整研究判断、条件和必要绑定值，不开放其他成员的私有记录' },
  { key: 'strategies', label: '策略', hint: '只读展示主人已分享的完整策略、触发条件和必要绑定值，不开放其他成员的私有记录' },
  { key: 'diagram', label: '产业图', hint: '完整产业图及其绑定的必要指标数值；不开放全库浏览' },
  { key: 'reports', label: '日报', hint: '只读展示主人已分享的完整日报和必要证据，不开放其他成员的私有记录' },
] as const

export type SourceSharingModule = typeof SOURCE_SHARING_MODULES[number]['key']

export function sourceSharingModulesDraft(value: unknown): SourceSharingModule[] {
  if (!Array.isArray(value)) return []
  return SOURCE_SHARING_MODULES.map(item => item.key).filter(key => value.includes(key))
}
