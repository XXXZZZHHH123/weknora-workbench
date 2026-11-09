<script setup lang="ts">
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
        <template #trigger><span class="picture-preview-trigger" aria-hidden="true"></span></template>
    </t-image-viewer>
</template>
<style scoped>
/* Opened programmatically. Its empty inline trigger must not create a line
   below the answer; the viewer itself is teleported to body. */
.picture-preview-trigger { display: none; }
</style>
