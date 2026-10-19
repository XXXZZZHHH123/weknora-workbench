<template>
  <span class="brand-wordmark" :class="{ 'brand-wordmark--compact': compact }" aria-label="中哲物产 · 研究工作台">
    <strong>中哲物产</strong>
    <span>研究工作台</span>
  </span>
</template>

<script setup lang="ts">
defineProps<{ compact?: boolean }>()
</script>

<style scoped>
@font-face {
  font-family: 'WeKnora Noto Serif SC';
  font-style: normal;
  font-weight: 600;
  font-display: swap;
  src: url('@/assets/fonts/brand-noto-serif-sc.woff2') format('woff2');
  unicode-range: U+4E2D, U+54F2, U+7269, U+4EA7;
}

.brand-wordmark { display: inline-flex; flex-direction: column; gap: var(--app-space-1); text-align: left; white-space: nowrap; }
.brand-wordmark strong { color: var(--td-text-color-primary); font-family: var(--cds-font-display); font-size: var(--app-text-3xl); font-weight: 600; letter-spacing: .04em; line-height: 1.15; }
.brand-wordmark > span { color: var(--td-text-color-secondary); font-size: var(--app-text-sm); font-weight: 400; letter-spacing: .12em; }
.brand-wordmark--compact strong { font-size: var(--app-text-3xl); }
.brand-wordmark--compact > span { font-size: var(--app-text-2xs); letter-spacing: .1em; }
</style>
