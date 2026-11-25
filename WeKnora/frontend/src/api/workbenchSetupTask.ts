export const workbenchSetupTask = {
  purpose: 'workbench_setup',
  title: '从周报建立工作台',
  completionTitle: '工作台草稿已生成',
  view: 'home',
} as const

export const workbenchSetupPhases: Readonly<Partial<Record<string, string>>> = {
  queued: '等待建立',
  reading_source: '读取周报',
  dimensions: '归纳维度',
  indicators: '建议指标',
  charts: '识别图表',
  layout: '编排周报版式',
  focus: '核心关注建议',
  diagram: '产业图骨架',
  judgment: '研判草稿',
  strategy: '策略草稿',
  monitors: '监测候选',
  succeeded: '草稿待核对',
  failed: '建立未完成',
  interrupted: '建立已中断',
  expired: '结果已过期',
  superseded: '已有更新结果',
}
