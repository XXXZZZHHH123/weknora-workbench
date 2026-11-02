<script setup lang="ts">
import { computed, h, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useRouter } from 'vue-router'
import { NotifyPlugin } from 'tdesign-vue-next'
import { useAnalysisTasksStore } from '@/stores/analysisTasks'
import { analysisPhaseLabel } from '@/stores/analysisTasksState'
import type { AnalysisJob } from '@/api/researchAnalysisTasks'

const store = useAnalysisTasksStore(), router = useRouter(), expanded = ref(false)
const rows = computed(() => store.jobs.filter(job => job.state !== 'applied' && job.state !== 'superseded' && !job.dismissed))
const label = computed(() => store.activeCount ? `研究分析进行中：${store.activeCount} 项`
  : store.unreadIds.length ? `研究分析：${store.unreadIds.length} 项待查看` : `研究分析：${rows.value.length} 项记录`)
const notifications = new Map<string, ReturnType<typeof NotifyPlugin.success>>()
const isRunning = (job: AnalysisJob) => job.state === 'queued' || job.state === 'running'
function closeNotice(id?: string) {
  for (const [key, notification] of notifications) {
    if (id !== undefined && key !== id) continue
    notifications.delete(key); NotifyPlugin.close(notification)
  }
}
function openTask(job: AnalysisJob) {
  if (!store.jobs.some(row => row.id === job.id && row.workbench_id === job.workbench_id && !row.dismissed && row.state !== 'superseded' && row.state !== 'applied')) return
  closeNotice(job.id)
  store.markRead(job.id); expanded.value = false
  void router.push({ name: 'researchWorkbenchDetail', params: { workbenchId: job.workbench_id }, query: { view: job.purpose === 'diagram' ? 'diagram' : 'judgment', analysis_job: job.id } })
}
async function ignoreTask(job: AnalysisJob) {
  if (isRunning(job) || !rows.value.some(row => row.id === job.id && row.workbench_id === job.workbench_id)) return
  const scope = store.scopeVersion
  const ignored = await store.dismiss(job.id)
  if (scope !== store.scopeVersion) return
  if (ignored) closeNotice(job.id)
  else expanded.value = true
}
function notifyCompleted() {
  if (document.visibilityState === 'hidden') return
  const completed = store.takeNotifications()
  for (const job of completed) {
    if (notifications.has(job.id)) continue
    const forget = (): void => { if (notifications.get(job.id) === notification) notifications.delete(job.id) }
    const notification = NotifyPlugin.success({ title: job.purpose === 'diagram' ? '产业图配置已生成' : '研究分析已完成',
      content: () => h('div', { class: 'analysis-completion' }, [h('span', '结果已暂存，尚未保存。'),
        h('button', { type: 'button', onClick: () => openTask(job) }, '查看结果'),
        h('button', { type: 'button', disabled: store.dismissingIds.includes(job.id), onClick: () => ignoreTask(job) }, '忽略')]),
      duration: 6000, closeBtn: true, onClose: forget, onCloseBtnClick: forget, onDurationEnd: forget })
    notifications.set(job.id, notification)
  }
}
function wake() { void store.refresh(); notifyCompleted() }
watch(() => store.pendingNotifications.length, notifyCompleted, { flush: 'post' })
watch(() => store.visible, visible => { if (!visible) { expanded.value = false; closeNotice() } })
watch(() => store.scopeVersion, () => closeNotice(), { flush: 'sync' })
onMounted(() => { void store.start(); document.addEventListener('visibilitychange', wake); window.addEventListener('focus', wake); notifyCompleted() })
onBeforeUnmount(() => { closeNotice(); store.stop(); document.removeEventListener('visibilitychange', wake); window.removeEventListener('focus', wake) })
</script>

<template>
  <section v-if="store.visible" class="analysis-tasks" aria-label="后台研究分析">
    <div v-if="expanded" id="research-analysis-tasks-list" class="analysis-tasks-list">
      <header><strong>研究分析</strong><button type="button" aria-label="收起分析进度" @click="expanded = false"><t-icon name="close" /></button></header>
      <p v-if="store.error" class="analysis-task-error" role="status">{{ store.error }}</p>
      <ul>
        <li v-for="job in rows" :key="job.id">
          <button type="button" class="analysis-task-row" @click="openTask(job)">
            <t-icon :name="job.state === 'queued' || job.state === 'running' ? 'loading' : job.state === 'succeeded' ? 'check-circle' : 'info-circle'" :class="{ 'is-running': job.state === 'queued' || job.state === 'running' }" />
            <span><strong>{{ job.purpose === 'diagram' ? '产业图配置生成' : '研究逻辑分析' }}</strong><small>{{ analysisPhaseLabel(job) }}</small></span>
            <span class="analysis-task-action">{{ job.result_available ? '查看结果' : '查看进度' }}</span>
            <i v-if="store.unreadIds.includes(job.id)" class="analysis-unread" aria-label="未读" />
          </button>
          <button v-if="!isRunning(job)" type="button" class="analysis-task-ignore" :disabled="store.dismissingIds.includes(job.id)" :aria-label="`忽略${job.title}提示`" @click="ignoreTask(job)">忽略</button>
        </li>
      </ul>
    </div>
    <button type="button" class="analysis-task-trigger" :aria-label="label" :title="label" :aria-expanded="expanded" aria-controls="research-analysis-tasks-list" @click="expanded = !expanded">
      <t-icon :name="store.activeCount ? 'loading' : 'task'" :class="{ 'is-running': store.activeCount > 0 }" size="20px" />
      <span v-if="store.unreadIds.length" class="analysis-trigger-unread" aria-hidden="true">{{ store.unreadIds.length }}</span>
    </button>
  </section>
</template>

<style scoped>
.analysis-tasks{display:flex;flex-direction:column;align-items:flex-end;gap:8px;min-height:42px;max-width:calc(100vw - 32px);pointer-events:auto;font-family:var(--app-font-family)}
.analysis-task-trigger{position:relative;display:grid;place-items:center;width:42px;height:42px;padding:0;border:1px solid var(--app-border-color);border-radius:50%;background:var(--app-card-bg);color:var(--td-brand-color);box-shadow:var(--td-shadow-2);cursor:pointer}
.analysis-trigger-unread{position:absolute;right:-3px;top:-4px;min-width:15px;padding:1px 3px;border-radius:var(--app-radius-lg);background:var(--td-brand-color);color:var(--td-text-color-anti);font-size:var(--app-text-2xs);line-height:15px}
.analysis-tasks-list{width:340px;max-width:calc(100vw - 32px);max-height:320px;overflow:auto;border:1px solid var(--app-border-color);border-radius:var(--app-radius-lg);background:var(--app-card-bg);box-shadow:var(--td-shadow-2);color:var(--app-text-primary)}
.analysis-tasks-list header{display:flex;align-items:center;justify-content:space-between;padding:10px 12px;border-bottom:1px solid var(--app-border-color);font-size:var(--app-text-sm)}
.analysis-tasks-list header button{display:grid;place-items:center;padding:3px;border:0;background:transparent;color:var(--app-text-secondary);cursor:pointer}
.analysis-tasks-list ul{list-style:none;margin:0;padding:4px}
.analysis-tasks-list li{display:flex;align-items:center}
.analysis-task-row{display:flex;align-items:center;gap:9px;flex:1;min-width:0;padding:10px 8px;border:0;border-radius:var(--app-radius-sm);background:transparent;color:inherit;text-align:left;font:inherit;cursor:pointer}
.analysis-task-ignore{flex-shrink:0;margin-right:8px;padding:4px;border:0;background:transparent;color:var(--app-text-secondary);font:inherit;font-size:var(--app-text-xs);cursor:pointer}.analysis-task-ignore:hover{color:var(--td-brand-color)}.analysis-task-ignore:disabled,.analysis-completion button:disabled{opacity:.6;cursor:wait}
.analysis-task-row:hover{background:var(--td-bg-color-container-hover)}
.analysis-task-row>span:first-of-type{display:grid;gap:3px;min-width:0}.analysis-task-row strong{font-size:var(--app-text-sm);font-weight:500}.analysis-task-row small{color:var(--app-text-secondary);font-size:var(--app-text-xs)}.analysis-task-action{margin-left:auto;color:var(--td-brand-color);font-size:var(--app-text-xs);white-space:nowrap}
.analysis-unread{width:6px;height:6px;border-radius:50%;background:var(--td-brand-color)}
.analysis-task-error{margin:8px 12px;color:var(--app-text-secondary);font-size:var(--app-text-xs)}
.analysis-tasks button:focus-visible{outline:2px solid var(--td-brand-color);outline-offset:2px}
.is-running{animation:analysis-task-spin 1s linear infinite}@keyframes analysis-task-spin{to{transform:rotate(360deg)}}
@media(prefers-reduced-motion:reduce){.is-running{animation:none}}
</style>
<style>
.analysis-completion{display:flex;align-items:center;gap:10px;font-size:var(--app-text-sm)}.analysis-completion button{border:0;padding:2px 0;background:transparent;color:var(--td-brand-color);font:inherit;cursor:pointer;text-decoration:underline;text-underline-offset:3px}
</style>
