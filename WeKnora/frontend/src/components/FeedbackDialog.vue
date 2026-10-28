<template>
  <!-- 全局「反馈与建议」弹窗：由右下角悬浮按钮打开，任何登录后页面可用。
       提交时附带当前页面地址和标题，方便管理员在「系统管理 → 用户反馈」里定位。 -->
  <t-dialog :visible="visible" width="520px" class="feedback-dialog" :on-confirm="handleSubmit" :on-close="handleClose"
    :confirm-btn="{ content: '提交反馈', loading: submitting, theme: 'primary', disabled: !canSubmit }"
    cancel-btn="取消" :close-on-overlay-click="!submitting" :close-on-esc-keydown="!submitting"
    @update:visible="onVisibleUpdate">
    <template #header>
      <div class="feedback-dialog-header">
        <span class="feedback-dialog-header__title">反馈与建议</span>
        <span class="feedback-dialog-header__sub">遇到问题或有改进想法都可以告诉我们，管理员会看到并跟进。</span>
      </div>
    </template>

    <div class="feedback-body">
      <div class="feedback-kinds" role="radiogroup" aria-label="反馈类型">
        <button v-for="option in kindOptions" :key="option.value" type="button" role="radio"
          class="feedback-kind" :class="{ 'is-active': form.kind === option.value }"
          :aria-checked="form.kind === option.value" @click="form.kind = option.value">
          <span class="feedback-kind__icon"><t-icon :name="option.icon" size="18px" aria-hidden="true" /></span>
          <span class="feedback-kind__text">
            <span class="feedback-kind__title">{{ option.title }}</span>
            <span class="feedback-kind__desc">{{ option.desc }}</span>
          </span>
          <t-icon v-if="form.kind === option.value" name="check-circle-filled" size="16px"
            class="feedback-kind__check" aria-hidden="true" />
        </button>
      </div>

      <t-textarea v-model="form.content" class="feedback-content" :placeholder="placeholder" :maxlength="5000"
        :autosize="{ minRows: 5, maxRows: 12 }" autofocus />

      <div class="feedback-page" :title="page.url">
        <t-icon name="link" size="14px" aria-hidden="true" />
        <span class="feedback-page__text">将附带当前页面：{{ pageLabel }}</span>
      </div>
    </div>
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
const kindOptions: { value: FeedbackKind; title: string; desc: string; icon: string }[] = [
  { value: 'bug', title: '报告问题', desc: '报错、功能异常或结果不对', icon: 'error-circle' },
  { value: 'suggestion', title: '提出建议', desc: '新功能或体验改进的想法', icon: 'lightbulb' },
]
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
  display: flex;
  flex-direction: column;
  gap: 4px;
}

.feedback-dialog-header__title {
  font-size: var(--app-text-xl);
  font-weight: 600;
  color: var(--td-text-color-primary);
}

.feedback-dialog-header__sub {
  font-size: var(--app-text-sm);
  font-weight: 400;
  line-height: 1.5;
  color: var(--td-text-color-secondary);
}

.feedback-body {
  display: flex;
  flex-direction: column;
  gap: 14px;
  padding-top: 4px;
}

.feedback-kinds {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 10px;
}

.feedback-kind {
  position: relative;
  display: flex;
  align-items: flex-start;
  gap: 10px;
  min-width: 0;
  padding: 12px 14px;
  border: 1px solid var(--td-component-stroke);
  border-radius: var(--app-radius-lg);
  background: var(--td-bg-color-container);
  color: var(--td-text-color-primary);
  text-align: left;
  cursor: pointer;
  transition: border-color var(--app-motion-base) ease, background-color var(--app-motion-base) ease,
    box-shadow var(--app-motion-base) ease;

  &:hover {
    border-color: var(--td-component-border);
    background: var(--td-bg-color-container-hover);
  }

  &:focus-visible {
    outline: 2px solid var(--td-brand-color);
    outline-offset: 2px;
  }

  &.is-active {
    border-color: var(--td-brand-color);
    background: var(--td-brand-color-light);
    box-shadow: 0 0 0 1px var(--td-brand-color) inset;
  }
}

.feedback-kind__icon {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  flex-shrink: 0;
  width: 30px;
  height: 30px;
  border-radius: 8px;
  background: var(--td-bg-color-secondarycontainer);
  color: var(--td-text-color-secondary);

  .is-active & {
    background: var(--td-bg-color-container);
    color: var(--td-brand-color);
  }
}

.feedback-kind__text {
  display: flex;
  flex-direction: column;
  gap: 2px;
  min-width: 0;
}

.feedback-kind__title {
  font-size: var(--app-text-md);
  font-weight: 500;
}

.feedback-kind__desc {
  font-size: var(--app-text-sm);
  line-height: 1.45;
  color: var(--td-text-color-secondary);
}

.feedback-kind__check {
  position: absolute;
  top: 10px;
  right: 10px;
  color: var(--td-brand-color);
}

.feedback-content {
  :deep(.t-textarea__inner) {
    border-radius: var(--app-radius-lg);
    padding: 10px 12px;
    line-height: 1.6;
  }
}

.feedback-page {
  display: flex;
  align-items: center;
  gap: 6px;
  min-width: 0;
  padding: 6px 10px;
  border-radius: var(--app-radius-md);
  background: var(--td-bg-color-secondarycontainer);
  font-size: var(--app-text-sm);
  color: var(--td-text-color-placeholder);
}

.feedback-page__text {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
</style>
