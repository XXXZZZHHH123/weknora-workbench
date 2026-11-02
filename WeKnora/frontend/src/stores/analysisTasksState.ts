import { computed, ref, watch } from 'vue'
import { analysisJobId, type AnalysisJob, type AnalysisTaskOwner } from '../api/researchAnalysisTasks'

interface Timers { setTimer: (callback: () => void, delay: number) => unknown; clearTimer: (handle: unknown) => void }
interface Dependencies {
  owner: () => AnalysisTaskOwner | null
  fetchJobs: (owner: AnalysisTaskOwner, isCurrent: () => boolean) => Promise<AnalysisJob[]>
  dismissJob?: (owner: AnalysisTaskOwner, id: string, isCurrent: () => boolean) => Promise<AnalysisJob>
  storage?: Pick<Storage, 'getItem' | 'setItem'>
  isVisible?: () => boolean
  timers?: Timers
}
const active = (job: AnalysisJob) => job.state === 'queued' || job.state === 'running'
const needsRead = (job: AnalysisJob) => ['succeeded', 'failed', 'interrupted', 'expired'].includes(job.state)
const keyFor = (owner: AnalysisTaskOwner | null) => owner?.token && owner.userId && /^[1-9][0-9]*$/.test(owner.tenantId)
  && Number.isSafeInteger(Number(owner.tenantId)) ? `weknora:analysis-tasks:${encodeURIComponent(owner.userId)}:${owner.tenantId}` : ''

export function analysisPhaseLabel(job: AnalysisJob): string {
  if (job.purpose === 'diagram') {
    const diagramPhases: Record<string, string> = { queued: '等待生成', reading_source: '读取原件', loading_catalog: '读取指标目录',
      selecting: '匹配指标', generating: '生成图结构', validating: '核对配置', succeeded: '配置已生成',
      failed: '生成未完成', interrupted: '生成已中断', expired: '结果已过期', applied: '已导入配置', superseded: '已有更新结果' }
    return diagramPhases[active(job) ? job.phase : job.state] || '后台生成中'
  }
  const phases: Record<string, string> = { queued: '等待分析', selecting: '核对资料范围', analyzing: '正在分析', validating: '核对分析结果',
    succeeded: '分析完成', failed: '分析未完成', interrupted: '分析已中断', expired: '结果已过期', applied: '已保存草稿', superseded: '已有更新结果' }
  return active(job) ? phases[job.phase] || '后台运行中' : phases[job.state] || '后台运行中'
}

function latestAvailableResults(rows: AnalysisJob[]): AnalysisJob[] {
  const latest = new Map<string, AnalysisJob>()
  for (const job of rows) {
    if (job.state !== 'succeeded' || !job.result_available) continue
    const key = JSON.stringify([job.workbench_id, job.purpose || 'judgments']), previous = latest.get(key)
    if (!previous || Date.parse(job.created_at) > Date.parse(previous.created_at)
      || job.created_at === previous.created_at && Date.parse(job.updated_at) > Date.parse(previous.updated_at)) latest.set(key, job)
  }
  return rows.filter(job => job.state !== 'succeeded' || !job.result_available
    || latest.get(JSON.stringify([job.workbench_id, job.purpose || 'judgments']))?.id === job.id)
}

export function createAnalysisTasks(deps: Dependencies) {
  const jobs = ref<AnalysisJob[]>([]), unreadIds = ref<string[]>([]), pendingNotifications = ref<AnalysisJob[]>([]), error = ref('')
  const dismissingIds = ref<string[]>([])
  const scopeVersion = ref(0)
  const seen = new Set<string>(), notified = new Set<string>(), dismissed = new Set<string>()
  const timers: Timers = deps.timers || { setTimer: (callback, delay) => setTimeout(callback, delay), clearTimer: handle => clearTimeout(handle as ReturnType<typeof setTimeout>) }
  let timer: unknown, started = false, blocked = false, generation = 0, currentKey = '', failures = 0
  let pending: { generation: number; promise: Promise<void> } | null = null
  const activeCount = computed(() => jobs.value.filter(active).length)
  const visible = computed(() => jobs.value.some(job => !job.dismissed && job.state !== 'applied' && job.state !== 'superseded'))
  const clearTimer = () => { if (timer !== undefined) timers.clearTimer(timer); timer = undefined }
  function persist() {
    if (!currentKey) return
    try { deps.storage?.setItem(currentKey, JSON.stringify({ seen: [...seen].slice(-200), notified: [...notified].slice(-200),
      ...(dismissed.size ? { dismissed: [...dismissed].slice(-200) } : {}) })) } catch { /* Server discovery remains available without browser storage. */ }
  }
  function restore() {
    seen.clear(); notified.clear(); dismissed.clear()
    if (!currentKey) return
    try {
      const value = JSON.parse(deps.storage?.getItem(currentKey) || '{}')
      for (const [name, target] of [['seen', seen], ['notified', notified], ['dismissed', dismissed]] as const) {
        if (Array.isArray(value[name])) for (const id of value[name].slice(-200)) if (analysisJobId(id)) target.add(id)
      }
    } catch { /* Ignore invalid local read markers; the server owns all task state. */ }
  }
  function schedule() {
    clearTimer()
    if (!started || blocked || !currentKey || deps.isVisible?.() === false) return
    const delay = failures ? 30000 : activeCount.value ? 5000 : 30000
    timer = timers.setTimer(() => { timer = undefined; void refresh() }, delay)
  }
  async function refresh(): Promise<void> {
    if (!started || blocked || !currentKey || deps.isVisible?.() === false) return
    if (pending?.generation === generation) return pending.promise
    clearTimer()
    const owner = deps.owner(), epoch = generation, scope = currentKey
    if (!owner || keyFor(owner) !== scope) return
    const isCurrent = () => started && epoch === generation && scope === keyFor(deps.owner())
    const promise = (async () => {
      try {
        const fetched = await deps.fetchJobs(owner, isCurrent)
        if (!isCurrent()) return
        let newMarker = false
        const consistent = fetched.map(job => {
          const previous = jobs.value.find(row => row.id === job.id && row.workbench_id === job.workbench_id)
          return previous && !active(previous) && active(job) ? previous : job
        })
        const result = latestAvailableResults(consistent).map(job => {
          if (active(job)) return { ...job, dismissed: false }
          if (job.dismissed && !dismissed.has(job.id)) { dismissed.add(job.id); newMarker = true }
          return dismissed.has(job.id) ? { ...job, dismissed: true } : job
        })
        if (newMarker) persist()
        jobs.value = result; failures = 0; error.value = ''
        unreadIds.value = result.filter(job => needsRead(job) && !job.dismissed && !seen.has(job.id)).map(job => job.id)
        pendingNotifications.value = pendingNotifications.value.filter(job => result.some(row => row.id === job.id && row.state === 'succeeded' && !row.dismissed) && !seen.has(job.id))
        for (const job of result) if (job.state === 'succeeded' && !job.dismissed && !seen.has(job.id) && !notified.has(job.id)
          && !pendingNotifications.value.some(row => row.id === job.id)) pendingNotifications.value.push(job)
      } catch (failure) {
        if (!isCurrent()) return
        const status = (failure as { status?: number })?.status
        error.value = '进度暂时无法更新。'
        failures++
        if (status === 401 || status === 403 || status === 404) {
          blocked = true; jobs.value = []; unreadIds.value = []; pendingNotifications.value = []
        }
      } finally {
        if (epoch === generation) { pending = null; schedule() }
      }
    })()
    pending = { generation: epoch, promise }
    return promise
  }
  watch(() => { const owner = deps.owner(); return [keyFor(owner), owner?.token || ''] }, ([key, token], previous) => {
    if (previous && key === previous[0] && token === previous[1]) return
    if (key !== currentKey) {
      clearTimer(); generation++; scopeVersion.value++; pending = null; currentKey = key || ''; blocked = false; failures = 0
      jobs.value = []; unreadIds.value = []; pendingNotifications.value = []; dismissingIds.value = []; error.value = ''; restore()
    } else if (previous && blocked && token !== previous[1]) { blocked = false; failures = 0 }
    if (started && currentKey) void refresh()
  }, { flush: 'sync', immediate: true })
  function start() { if (started) return refresh(); started = true; return refresh() }
  function stop() { started = false; clearTimer(); generation++; pending = null; dismissingIds.value = [] }
  function markRead(id: string) {
    const job = jobs.value.find(row => row.id === id)
    if (!job || active(job)) return
    seen.add(id); unreadIds.value = unreadIds.value.filter(value => value !== id)
    pendingNotifications.value = pendingNotifications.value.filter(value => value.id !== id); persist()
  }
  async function dismiss(id: string): Promise<boolean> {
    const job = jobs.value.find(row => row.id === id), owner = deps.owner(), epoch = generation, scope = currentKey
    if (!started || !job || active(job) || !owner || keyFor(owner) !== scope || !deps.dismissJob || dismissingIds.value.includes(id)) return false
    if (job.dismissed) return true
    const isCurrent = () => started && epoch === generation && scope === keyFor(deps.owner())
    dismissingIds.value.push(id)
    try {
      const acknowledged = await deps.dismissJob(owner, id, isCurrent)
      if (!isCurrent()) return false
      if (acknowledged.id !== id || acknowledged.workbench_id !== job.workbench_id
        || (acknowledged.purpose || 'judgments') !== (job.purpose || 'judgments') || acknowledged.dismissed !== true || active(acknowledged)) throw new Error('忽略状态未能核对。')
      dismissed.add(id); seen.add(id)
      jobs.value = jobs.value.map(row => row.id === id ? { ...row, dismissed: true } : row)
      unreadIds.value = unreadIds.value.filter(value => value !== id)
      pendingNotifications.value = pendingNotifications.value.filter(value => value.id !== id)
      error.value = ''; persist(); return true
    } catch {
      if (isCurrent()) error.value = '暂时无法忽略此提示，请稍后重试。'
      return false
    } finally {
      if (isCurrent()) dismissingIds.value = dismissingIds.value.filter(value => value !== id)
    }
  }
  function takeNotifications() {
    const result = pendingNotifications.value.slice(); pendingNotifications.value = []
    for (const job of result) notified.add(job.id)
    persist(); return result
  }
  return { jobs, unreadIds, pendingNotifications, dismissingIds, error, scopeVersion, activeCount, visible, start, stop, refresh, markRead, dismiss, takeNotifications }
}
