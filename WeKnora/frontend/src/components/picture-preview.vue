<script setup lang="ts">
const props = defineProps(['reviewImg', 'reviewUrl'])
const emit = defineEmits(['closePreImg'])
const close = () => {
    emit('closePreImg')
}
</script>
<template>
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
