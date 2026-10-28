<template>
  <!-- 全局右下角「反馈」悬浮入口：登录后的所有页面都可见，登录/注册等公开页不显示。 -->
  <template v-if="enabled">
    <button type="button" class="feedback-launcher" :class="{ 'is-open': visible, 'is-raised': onChatRoute }" aria-label="反馈与建议"
      title="报告问题或提出建议" @click="visible = true">
      <t-icon name="chat-bubble-smile" size="18px" aria-hidden="true" />
      <span class="feedback-launcher__label">反馈</span>
    </button>
    <FeedbackDialog v-model:visible="visible" />
  </template>
</template>

<script setup lang="ts">
import { computed, ref } from 'vue'
import { useRoute } from 'vue-router'
import { useAuthStore } from '@/stores/auth'
import FeedbackDialog from './FeedbackDialog.vue'

const route = useRoute()
const authStore = useAuthStore()
const visible = ref(false)

// 对话页底部是输入框，按钮上移避免挡住发送键。
const CHAT_ROUTES = new Set(['chat', 'kbChat', 'globalCreatChat', 'kbCreatChat'])
const onChatRoute = computed(() => CHAT_ROUTES.has(String(route.name)))
const enabled = computed(() => authStore.isLoggedIn && route.matched.some((r) => r.meta.requiresAuth))
</script>

<style lang="less" scoped>
.feedback-launcher {
  position: fixed;
  right: 24px;
  bottom: 24px;
  // 低于 TDesign 抽屉/弹窗（1500+），打开抽屉时不压住其底部按钮。
  z-index: 1400;
  display: inline-flex;
  align-items: center;
  gap: 6px;
  height: 40px;
  padding: 0 16px 0 14px;
  border: none;
  border-radius: 999px;
  background: var(--td-brand-color);
  color: var(--td-text-color-anti);
  font-size: var(--app-text-md);
  font-weight: 500;
  letter-spacing: 0.02em;
  box-shadow: var(--cds-shadow-lg);
  cursor: pointer;
  transition: transform var(--app-motion-base) cubic-bezier(0.16, 1, 0.3, 1),
    box-shadow var(--app-motion-base) ease, background-color var(--app-motion-base) ease, opacity var(--app-motion-base) ease;

  &:hover {
    transform: translateY(-2px);
    background: var(--td-brand-color-hover);
  }

  &:active {
    transform: translateY(0);
    background: var(--td-brand-color-active);
  }

  &:focus-visible {
    outline: 2px solid var(--td-brand-color);
    outline-offset: 3px;
  }

  &.is-raised {
    bottom: 160px;
  }

  &.is-open {
    opacity: 0;
    pointer-events: none;
  }
}

@media (max-width: 768px) {
  .feedback-launcher {
    right: 16px;
    bottom: 16px;
    width: 44px;
    height: 44px;
    padding: 0;
    justify-content: center;
  }

  .feedback-launcher__label {
    display: none;
  }
}

@media print {
  .feedback-launcher {
    display: none;
  }
}
</style>
