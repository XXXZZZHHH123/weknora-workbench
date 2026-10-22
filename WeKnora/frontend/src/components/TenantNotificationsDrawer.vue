<template>
  <SettingDrawer v-model:visible="visible" title="空间通知" icon="notification" width="460px"
    :min-width="360" :resizable="false" hide-footer description="成员管理变更，仅展示发给你的通知。">
    <template #header-actions>
      <t-button size="small" theme="default" variant="text" :loading="state.loading" @click="refresh">
        <template #icon><t-icon name="refresh" /></template>刷新
      </t-button>
    </template>
    <div v-if="state.error" class="notification-message" role="status">{{ state.error }}</div>
    <div v-if="state.loading && !state.rows.length" class="notification-empty"><t-loading size="small" text="加载通知…" /></div>
    <div v-else-if="!state.rows.length && !state.error" class="notification-empty">暂无通知</div>
    <ol v-else class="notification-list">
      <li v-for="row in state.rows" :key="row.id" :class="{ 'is-unread': !row.read_at }">
        <div class="notification-title">
          <span>{{ actionLabel(row.action) }}</span><span v-if="!row.read_at" class="notification-dot" aria-label="未读" />
        </div>
        <p><span class="notification-actor">{{ row.actor_name || row.actor_user_id }}</span>
          <span v-if="row.target_user_id"> · {{ row.target_name || row.target_user_id }}</span>
        </p>
        <p v-if="row.details?.old_role && row.details?.new_role" class="notification-role">
          {{ roleLabel(row.details.old_role) }} → {{ roleLabel(row.details.new_role) }}
        </p>
        <div class="notification-meta">
          <time :datetime="row.created_at">{{ formatTime(row.created_at) }}</time>
          <t-button v-if="!row.read_at" size="small" variant="text" theme="default"
            :loading="state.pendingReads.includes(row.id)" @click="inbox.read(tenantId, row.id)">标记已读</t-button>
          <span v-else>已读</span>
        </div>
      </li>
    </ol>
    <t-button v-if="state.cursor" class="notification-more" size="small" variant="text" theme="default"
      :loading="state.loading" @click="inbox.load(tenantId, true)">加载更早通知</t-button>
  </SettingDrawer>
</template>

<script setup lang="ts">
import { computed, onMounted, onUnmounted, reactive, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useAuthStore } from '@/stores/auth'
import SettingDrawer from '@/components/settings/SettingDrawer.vue'
import { listWorkspaceNotifications, markWorkspaceNotificationRead } from '@/api/tenant/notifications'
import { createNotificationInbox, notificationInboxState } from '@/utils/workspaceNotifications'
import { auditActionLabel } from '@/i18n/auditActionLabel'
import { useRoleLabel } from '@/composables/useRoleLabel'

const props = defineProps<{ visible: boolean; menuOpen?: boolean }>()
const emit = defineEmits<{ 'update:visible': [value: boolean]; unread: [value: number] }>()
const auth = useAuthStore()
const i18n = useI18n()
const { formatRole } = useRoleLabel()
const tenantId = computed(() => Number(auth.effectiveTenantId) || 0)
const available = computed(() => auth.isLoggedIn && !auth.isLiteMode && tenantId.value > 0)
const visible = computed({ get: () => props.visible, set: (value) => emit('update:visible', value) })
const state = reactive(notificationInboxState())
const inbox = createNotificationInbox(state, listWorkspaceNotifications, markWorkspaceNotificationRead)
let timer: ReturnType<typeof setInterval> | undefined

const actionLabel = (action: string) => auditActionLabel(i18n, 'tenantMember.audit.action', action)
const roleLabel = (role: unknown) => typeof role === 'string' ? formatRole(role) : ''
const formatTime = (value: string) => {
  const date = new Date(value)
  return Number.isNaN(date.getTime()) ? '' : date.toLocaleString(undefined, { month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit' })
}
const refresh = () => { if (available.value) void inbox.load(tenantId.value, false, 50, true) }
const poll = () => {
  if (!document.hidden && available.value && auth.hasRole('admin') && !visible.value) void inbox.load(tenantId.value, false, 1)
}
watch(() => state.unread, (value) => emit('unread', value), { immediate: true })
watch(() => [tenantId.value, auth.user?.id, auth.token] as const, (identity, previous) => {
  inbox.reset()
  if (previous && (identity[0] !== previous[0] || identity[1] !== previous[1])) visible.value = false
  if (!available.value) visible.value = false
  else if (visible.value) refresh()
  else if (typeof document !== 'undefined') poll()
}, { immediate: true, flush: 'sync' })
watch(visible, (value) => { if (value) refresh() })
watch(() => props.menuOpen, (value) => { if (value && !visible.value) poll() })
onMounted(() => {
  timer = setInterval(poll, 60_000)
  document.addEventListener('visibilitychange', poll)
})
onUnmounted(() => {
  if (timer) clearInterval(timer)
  document.removeEventListener('visibilitychange', poll)
  inbox.reset()
})
</script>

<style scoped>
.notification-list { list-style: none; margin: 0; padding: 0; }
.notification-list li { padding: 14px 0; border-bottom: 1px solid var(--td-component-stroke); }
.notification-title { display: flex; align-items: center; gap: 8px; color: var(--td-text-color-primary); font-weight: 500; }
.notification-dot { width: 6px; height: 6px; flex-shrink: 0; border-radius: 50%; background: var(--td-brand-color); }
.notification-list p { margin: 6px 0; overflow-wrap: anywhere; color: var(--td-text-color-secondary); font-size: var(--app-text-sm); }
.notification-meta { display: flex; justify-content: space-between; align-items: center; min-height: 26px; gap: 8px; color: var(--td-text-color-placeholder); font-size: var(--app-text-xs); }
.notification-message, .notification-empty { padding: 24px 0; color: var(--td-text-color-secondary); font-size: var(--app-text-sm); }
.notification-more { display: flex; margin: 12px auto 0; }
</style>
