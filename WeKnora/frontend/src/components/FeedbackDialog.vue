<template>
  <!-- 全局「反馈与建议」弹窗：入口在左侧栏底部，任何页面都能打开。
       提交时附带当前页面地址和标题，方便管理员在「系统管理 → 用户反馈」里定位。 -->
  <t-dialog :visible="visible" width="520px" :on-confirm="handleSubmit" :on-close="handleClose"
    :confirm-btn="{ content: '提交', loading: submitting, theme: 'primary', disabled: !canSubmit }"
    cancel-btn="取消" :close-on-overlay-click="!submitting" :close-on-esc-keydown="!submitting"
    @update:visible="onVisibleUpdate">
    <template #header>
      <span class="feedback-dialog-header">
        <t-icon name="chat-bubble-help" size="20px" class="feedback-dialog-header-icon" aria-hidden="true" />
        <span>反馈与建议</span>
      </span>
    </template>

    <p class="feedback-tip">遇到问题或有改进想法都可以告诉我们，管理员会看到并跟进处理。</p>

    <t-form label-align="top" class="feedback-form" @submit.prevent>
      <t-form-item label="类型">
        <t-radio-group v-model="form.kind" variant="default-filled">
          <t-radio-button value="bug">报告问题</t-radio-button>
          <t-radio-button value="suggestion">提出建议</t-radio-button>
        </t-radio-group>
      </t-form-item>
      <t-form-item label="内容">
        <t-textarea v-model="form.content" :placeholder="placeholder" :maxlength="5000"
          :autosize="{ minRows: 5, maxRows: 12 }" autofocus />
      </t-form-item>
      <p class="feedback-page" :title="pageLabel">
        <t-icon name="link" size="14px" aria-hidden="true" />
        <span>将附带当前页面：{{ pageLabel }}</span>
      </p>
    </t-form>
  </t-dialog>
</template>

<script setup lang="ts">
import { computed, reactive, ref, watch } from 'vue'
import { useRoute } from 'vue-router'
import { MessagePlugin } from 'tdesign-vue-next'
import { submitFeedback, type FeedbackKind } from '@/api/feedback'

const props = defineProps<{ visible: boolean }>()
const emit = defineEmits<{ (e: 'update:visible', value: boolean): void }>()

const route = useRoute()
const submitting = ref(false)
// 草稿在关闭弹窗后保留，提交成功才清空，避免误关丢失长文本。
const form = reactive<{ kind: FeedbackKind; content: string }>({ kind: 'bug', content: '' })
const page = reactive({ url: '', title: '' })

const canSubmit = computed(() => form.content.trim().length > 0)
const placeholder = computed(() => form.kind === 'bug'
  ? '请描述遇到的问题：做了什么操作、看到了什么、期望是什么。'
  : '请描述你的建议：希望改进哪里、为什么。')
const pageLabel = computed(() => page.title || page.url || '—')

watch(() => props.visible, (open) => {
  if (!open) return
  page.url = route.fullPath
  page.title = document.title
}, { immediate: true })

function onVisibleUpdate(value: boolean) {
  if (!value && submitting.value) return
  emit('update:visible', value)
}

function handleClose() {
  if (submitting.value) return
  emit('update:visible', false)
}

async function handleSubmit() {
  if (!canSubmit.value || submitting.value) return
  submitting.value = true
  try {
    await submitFeedback({ kind: form.kind, content: form.content.trim(), page_url: page.url, page_title: page.title })
    MessagePlugin.success('已提交，感谢反馈')
    form.content = ''
    emit('update:visible', false)
  } catch (err: any) {
    MessagePlugin.error(err?.message || '提交失败，请稍后重试')
  } finally {
    submitting.value = false
  }
}
</script>

<style lang="less" scoped>
.feedback-dialog-header {
  display: inline-flex;
  align-items: center;
  gap: 8px;
}

.feedback-dialog-header-icon {
  flex-shrink: 0;
  color: var(--td-brand-color);
}

.feedback-tip {
  margin: 0 0 16px;
  font-size: var(--app-text-md);
  line-height: 1.55;
  color: var(--td-text-color-secondary);
}

.feedback-page {
  display: flex;
  align-items: center;
  gap: 6px;
  margin: 0;
  min-width: 0;
  font-size: var(--app-text-sm);
  color: var(--td-text-color-placeholder);

  span {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
}

.feedback-form {
  :deep(.t-form__item) {
    margin-bottom: 16px;
  }
}
</style>
