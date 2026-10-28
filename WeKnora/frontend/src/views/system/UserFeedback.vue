<template>
  <div class="user-feedback">
    <header class="section-header feedback-page-header">
      <div>
        <h2>用户反馈</h2>
        <p class="section-description">用户通过页面右下角「反馈」按钮提交的问题和建议，覆盖全部空间。</p>
      </div>
    </header>

    <div class="feedback-filters">
      <div class="fb-segmented" role="tablist" aria-label="处理状态">
        <button v-for="option in statusOptions" :key="option.value" type="button" role="tab"
          class="fb-segmented__item" :class="{ 'is-active': status === option.value }"
          :aria-selected="status === option.value" @click="selectStatus(option.value)">
          {{ option.label }}
          <span v-if="option.value === 'open' && openCount" class="fb-segmented__count">{{ openCount }}</span>
        </button>
      </div>
      <t-select v-model="kind" size="small" class="feedback-kind-select" @change="reload">
        <t-option value="" label="全部类型" />
        <t-option value="bug" label="问题" />
        <t-option value="suggestion" label="建议" />
      </t-select>
      <button type="button" class="fb-refresh" :disabled="loading" title="刷新" aria-label="刷新" @click="reload">
        <t-icon :name="loading ? 'loading' : 'refresh'" :class="{ 'fb-refresh-spin': loading }" />
      </button>
    </div>

    <t-alert v-if="error" theme="error" :message="error">
      <template #operation>
        <t-button size="small" @click="reload">重试</t-button>
      </template>
    </t-alert>
    <div v-else-if="!loading && items.length === 0" class="feedback-empty">
      <t-empty description="暂无反馈" />
    </div>
    <div v-else class="feedback-list narrow-scrollbar">
      <article v-for="item in items" :key="item.id" class="feedback-item">
        <div class="feedback-item__meta">
          <t-tag :theme="item.kind === 'bug' ? 'danger' : 'success'" size="small" variant="light">
            {{ item.kind === 'bug' ? '问题' : '建议' }}
          </t-tag>
          <span class="feedback-item__user" :title="item.email || item.user_id">
            {{ item.username || item.email || item.user_id.slice(0, 8) }}
          </span>
          <span v-if="item.tenant_name" class="feedback-item__tenant">· {{ item.tenant_name }}</span>
          <span class="feedback-item__time">{{ formatTime(item.created_at) }}</span>
          <t-button size="small" variant="text" :theme="item.status === 'open' ? 'primary' : 'default'"
            class="feedback-item__action" :loading="updatingId === item.id" @click="toggleStatus(item)">
            {{ item.status === 'open' ? '标记已处理' : '重新打开' }}
          </t-button>
        </div>
        <p class="feedback-item__content">{{ item.content }}</p>
        <div v-if="item.page_url || item.page_title" class="feedback-item__page">
          <t-icon name="browse" size="12px" aria-hidden="true" />
          <span class="feedback-item__page-text" :title="item.page_url">
            提交页面：{{ item.page_title || '—' }}<span v-if="item.page_url" class="feedback-item__page-path">{{ item.page_url }}</span>
          </span>
          <button v-if="item.page_url" type="button" class="feedback-item__copy" title="复制页面地址"
            aria-label="复制页面地址" @click="copyPageURL(item.page_url)">
            <t-icon name="file-copy" size="12px" />
          </button>
        </div>
      </article>
      <div v-if="loading" class="feedback-loading"><t-loading size="small" /></div>
      <t-button v-else-if="nextCursor" variant="text" block class="feedback-more" @click="loadMore">加载更多</t-button>
    </div>
  </div>
</template>

<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { MessagePlugin } from 'tdesign-vue-next'
import {
  listFeedback,
  setFeedbackStatus,
  type FeedbackEntry,
  type FeedbackKind,
  type FeedbackStatus,
} from '@/api/feedback'

const PAGE_SIZE = 30

const items = ref<FeedbackEntry[]>([])
const status = ref<FeedbackStatus | ''>('open')
const kind = ref<FeedbackKind | ''>('')
const loading = ref(false)
const error = ref('')
const nextCursor = ref(0)
const openCount = ref(0)
const updatingId = ref<number | null>(null)
let generation = 0

const statusOptions: { value: FeedbackStatus | ''; label: string }[] = [
  { value: 'open', label: '未处理' },
  { value: 'resolved', label: '已处理' },
  { value: '', label: '全部' },
]

function selectStatus(value: FeedbackStatus | '') {
  if (status.value === value) return
  status.value = value
  reload()
}

// 来源页面多为提交人自己空间里的地址（如对话），管理员直接打开会无权限或对不上，
// 所以只展示并提供复制，便于排查时对照。
async function copyPageURL(url: string) {
  const full = url.startsWith('/') ? `${window.location.origin}${url}` : url
  try {
    await navigator.clipboard.writeText(full)
    MessagePlugin.success('已复制页面地址')
  } catch {
    MessagePlugin.warning(full)
  }
}

async function fetchPage(afterID = 0) {
  const current = ++generation
  loading.value = true
  error.value = ''
  try {
    const page = await listFeedback({ status: status.value, kind: kind.value, after_id: afterID, limit: PAGE_SIZE })
    if (current !== generation) return
    items.value = afterID ? [...items.value, ...page.items] : page.items
    nextCursor.value = page.next_cursor
    openCount.value = page.open_count
  } catch (err: any) {
    if (current === generation) error.value = err?.message || '加载失败'
  } finally {
    if (current === generation) loading.value = false
  }
}

function reload() {
  items.value = []
  void fetchPage()
}

function loadMore() {
  if (nextCursor.value) void fetchPage(nextCursor.value)
}

async function toggleStatus(item: FeedbackEntry) {
  const target: FeedbackStatus = item.status === 'open' ? 'resolved' : 'open'
  updatingId.value = item.id
  try {
    await setFeedbackStatus(item.id, target)
    item.status = target
    openCount.value += target === 'open' ? 1 : -1
    if (status.value && status.value !== target) {
      items.value = items.value.filter((row) => row.id !== item.id)
    }
  } catch (err: any) {
    MessagePlugin.error(err?.message || '操作失败')
  } finally {
    updatingId.value = null
  }
}

function formatTime(s: string) {
  try {
    return new Intl.DateTimeFormat('zh-CN', {
      year: 'numeric', month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit', hour12: false,
    }).format(new Date(s))
  } catch {
    return s
  }
}

onMounted(reload)
</script>

<style lang="less" scoped>
@import (reference) '@/components/css/settings-section.less';

.user-feedback {
  width: 100%;
  display: flex;
  flex-direction: column;
  min-height: 0;
}

.section-header {
  .settings-section-header();
}

.feedback-page-header {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: 16px;

  h2 {
    margin: 0 0 8px;
    font-size: var(--app-text-3xl);
    font-weight: 600;
    color: var(--td-text-color-primary);
  }
}

.fb-refresh {
  margin-left: auto;
  display: inline-flex;
  align-items: center;
  justify-content: center;
  flex-shrink: 0;
  width: 28px;
  height: 28px;
  padding: 0;
  border: none;
  border-radius: var(--app-radius-sm);
  background: transparent;
  color: var(--td-text-color-placeholder);
  cursor: pointer;

  &:hover:not(:disabled) {
    color: var(--td-brand-color);
    background: var(--td-bg-color-secondarycontainer);
  }

  &:disabled {
    cursor: default;
    opacity: 0.7;
  }
}

.fb-refresh-spin {
  animation: wk-spin 0.8s linear infinite;
}

.feedback-filters {
  display: flex;
  align-items: center;
  gap: 12px;
  margin-bottom: 12px;
}

.feedback-kind-select {
  width: 120px;
}

.feedback-empty {
  display: flex;
  justify-content: center;
  align-items: center;
  min-height: 280px;
}

.feedback-list {
  max-height: calc(100vh - 300px);
  overflow-y: auto;
  display: flex;
  flex-direction: column;
  gap: 8px;
}

.feedback-item {
  padding: 12px 14px;
  border: 1px solid var(--td-component-stroke);
  border-radius: var(--app-radius-sm);
  background: var(--td-bg-color-container);
}

.feedback-item__meta {
  display: flex;
  align-items: center;
  gap: 8px;
  min-width: 0;
  font-size: var(--app-text-sm);
  color: var(--td-text-color-secondary);
}

.feedback-item__user {
  color: var(--td-text-color-primary);
  font-weight: 500;
}

.feedback-item__tenant {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.feedback-item__time {
  margin-left: auto;
  flex-shrink: 0;
  color: var(--td-text-color-placeholder);
}

.feedback-item__action {
  flex-shrink: 0;
}

.feedback-item__content {
  margin: 8px 0 6px;
  font-size: var(--app-text-md);
  line-height: 1.6;
  color: var(--td-text-color-primary);
  white-space: pre-wrap;
  word-break: break-word;
}

.feedback-item__page {
  display: flex;
  align-items: center;
  gap: 6px;
  min-width: 0;
  font-size: var(--app-text-sm);
  color: var(--td-text-color-placeholder);
}

.feedback-item__page-text {
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.feedback-item__page-path {
  margin-left: 6px;
  font-family: var(--td-font-family-mono, ui-monospace, monospace);
  opacity: 0.8;
}

.feedback-item__copy {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  flex-shrink: 0;
  width: 20px;
  height: 20px;
  padding: 0;
  border: none;
  border-radius: var(--app-radius-xs);
  background: transparent;
  color: var(--td-text-color-placeholder);
  cursor: pointer;

  &:hover {
    color: var(--td-brand-color);
    background: var(--td-bg-color-secondarycontainer);
  }
}

.fb-segmented {
  display: inline-flex;
  align-items: center;
  gap: 2px;
  padding: 3px;
  border-radius: var(--app-radius-md);
  background: var(--td-bg-color-secondarycontainer);
}

.fb-segmented__item {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  height: 26px;
  padding: 0 12px;
  border: none;
  border-radius: var(--app-radius-sm);
  background: transparent;
  color: var(--td-text-color-secondary);
  font-size: var(--app-text-sm);
  cursor: pointer;
  transition: background-color var(--app-motion-base) ease, color var(--app-motion-base) ease,
    box-shadow var(--app-motion-base) ease;

  &:hover {
    color: var(--td-text-color-primary);
  }

  &:focus-visible {
    outline: 2px solid var(--td-brand-color);
    outline-offset: 1px;
  }

  &.is-active {
    background: var(--td-bg-color-container);
    color: var(--td-text-color-primary);
    font-weight: 500;
    box-shadow: var(--cds-shadow-sm);
  }
}

.fb-segmented__count {
  min-width: 18px;
  height: 16px;
  padding: 0 5px;
  border-radius: 999px;
  background: var(--td-brand-color-light);
  color: var(--td-brand-color);
  font-size: 11px;
  line-height: 16px;
  text-align: center;
}

.feedback-loading {
  display: flex;
  justify-content: center;
  padding: 12px;
}
</style>
