<template>
  <div class="user-feedback">
    <header class="section-header feedback-page-header">
      <div>
        <h2>用户反馈</h2>
        <p class="section-description">用户通过左侧栏「反馈与建议」提交的问题和建议，覆盖全部空间。</p>
      </div>
      <button type="button" class="fb-refresh" :disabled="loading" title="刷新" aria-label="刷新" @click="reload">
        <t-icon :name="loading ? 'loading' : 'refresh'" :class="{ 'fb-refresh-spin': loading }" />
      </button>
    </header>

    <div class="feedback-filters">
      <t-radio-group v-model="status" variant="default-filled" size="small" @change="reload">
        <t-radio-button value="open">未处理{{ openCount ? ` (${openCount})` : '' }}</t-radio-button>
        <t-radio-button value="resolved">已处理</t-radio-button>
        <t-radio-button value="">全部</t-radio-button>
      </t-radio-group>
      <t-select v-model="kind" size="small" class="feedback-kind-select" @change="reload">
        <t-option value="" label="全部类型" />
        <t-option value="bug" label="问题" />
        <t-option value="suggestion" label="建议" />
      </t-select>
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
          <t-tag :theme="item.kind === 'bug' ? 'danger' : 'primary'" size="small" variant="light">
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
        <a v-if="item.page_url" class="feedback-item__page" :href="item.page_url" target="_blank" rel="noopener"
          :title="item.page_url">
          <t-icon name="link" size="12px" aria-hidden="true" />
          {{ item.page_title ? `${item.page_title} · ` : '' }}{{ item.page_url }}
        </a>
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
  display: inline-flex;
  align-items: center;
  justify-content: center;
  flex-shrink: 0;
  width: 20px;
  height: 20px;
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
  display: inline-flex;
  align-items: center;
  gap: 4px;
  max-width: 100%;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  font-size: var(--app-text-sm);
  color: var(--td-text-color-placeholder);
  text-decoration: none;

  &:hover {
    color: var(--td-brand-color);
  }
}

.feedback-loading {
  display: flex;
  justify-content: center;
  padding: 12px;
}
</style>
