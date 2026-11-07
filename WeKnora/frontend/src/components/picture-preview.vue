<script setup lang="ts">
import { watch } from "vue"

const props = defineProps(['reviewImg', 'reviewUrl'])
const emit = defineEmits(['closePreImg'])
const close = () => {
    emit('closePreImg')
}
</script>
<template>
    <!-- Opened programmatically. Without a trigger slot TDesign renders a default thumbnail of the
         first image, which shows as a broken "图片无法显示 / 预览" box when nothing is selected. -->
    <t-image-viewer :visible="reviewImg" closeOnOverlay closeOnEscKeydown @close="close"
        :images="reviewUrl ? [reviewUrl] : []">
        <template #trigger></template>
    </t-image-viewer>
</template>
<style scoped lang="less"></style>
