<template>
  <div v-if="plan" class="field-clarification-card">
    <strong>需要你确认字段</strong>
    <p>{{ plan.question }}</p>
    <t-button theme="primary" variant="outline" :disabled="!active || submitted" @click="visible = true">{{ submitted ? '已提交选择' : '选择字段或补充说明' }}</t-button>
    <t-dialog attach="body" placement="center" v-model:visible="visible" header="你想查询哪一项？" width="min(720px, 94vw)" :footer="false" :close-on-overlay-click="false">
      <div class="field-clarification-body" v-if="plan">
        <p>{{ plan.question }}</p>
        <fieldset v-for="field in visibleFields" :key="field.role">
          <legend>{{ field.label }}</legend>
          <label v-for="candidate in field.candidates" :key="candidate.series_id" class="field-candidate">
            <input type="radio" :name="`${plan.resolution_id}-${field.role}`" :value="candidate.series_id" v-model="selections[field.role]" />
            <span><strong>{{ candidate.metric }}<template v-if="dimensionLabel(candidate.dimensions)"> · {{ dimensionLabel(candidate.dimensions) }}</template></strong><small>工作表：{{ candidate.sheet }} · 单位：{{ candidate.unit || '原表未注明' }}<template v-if="candidate.header_cells?.length"> · 表头：{{ candidate.header_cells.join('、') }}</template></small><small v-if="candidate.chart_sheet">图表所在页：{{ candidate.chart_sheet }}</small><small v-if="candidate.reason">{{ candidate.reason }}</small></span>
          </label>
          <p v-if="!field.candidates.length">暂时没有足够明确的候选，请在下方补充说明。</p>
        </fieldset>
        <label for="field-clarification-note">也可以直接输入你的意思{{ needsText ? '（需要补充）' : '（可选）' }}</label>
        <textarea id="field-clarification-note" v-model="note" maxlength="1200" rows="3" placeholder="请补充你要查询的对象、范围或计量单位。" />
        <p class="field-help">候选依据来自模型判断。确认前不会执行这个待确认方案的取数或计算。</p>
        <p class="field-help" role="status">{{ submitHint }}</p>
        <div class="field-actions"><t-button variant="outline" @click="visible = false">稍后</t-button><t-button :disabled="!canSubmit || !active || submitted" @click="submit">确认并继续</t-button></div>
      </div>
    </t-dialog>
  </div>
</template>
<script setup lang="ts">
import { computed, ref, watch } from 'vue';
import { fieldClarification, confirmationText, dimensionLabel, canConfirmFields } from '@/utils/fieldClarification';
const props = defineProps<{ session: any; active: boolean }>();
const emit = defineEmits<{ submit: [text: string] }>();
const plan = computed(() => fieldClarification(props.session.agentEventStream || [], !!props.session.is_completed));
const visible = ref(false), submitted = ref(false), note = ref('');
const selections = ref<Record<string, string>>({});
const visibleFields=computed(()=>plan.value?.fields.filter(f=>!plan.value?.same_series_roles?.includes(f.role) || f.role===plan.value.same_series_roles[0]) || []);
const needsText = computed(() => !!plan.value?.requires_note && !plan.value.fields.some(f => f.candidates.length > 1));
const canSubmit = computed(() => canConfirmFields(plan.value, selections.value, note.value));
const submitHint = computed(() => canSubmit.value
  ? '可以提交当前选择或说明；如果仍缺计算口径，系统会继续询问。'
  : needsText.value ? '请在输入框补充上方问题所需的信息后继续。' : '请为每项用途选择一个字段，或输入补充说明。');
watch(() => plan.value?.resolution_id, () => {
  selections.value = {}; note.value = ''; submitted.value = false;
  for (const f of plan.value?.fields || []) if (f.candidates.length === 1) selections.value[f.role] = f.candidates[0].series_id;
}, { immediate: true });
// Keep a confirmation available without interrupting every assistant reply.
watch(() => plan.value?.resolution_id, () => { visible.value = false; });
function submit() {
  if (!plan.value || !canSubmit.value || !props.active || submitted.value) return;
  const text = confirmationText(plan.value, selections.value, note.value);
  submitted.value = true; visible.value = false; emit('submit', text);
}
</script>
<style scoped>
.field-clarification-card { margin: 12px 0; padding: 18px; border: 1px solid #b7d9ca; border-radius: var(--app-radius-xl); background: #f3faf6; color: #243d34; }
.field-clarification-card p { margin: 8px 0 14px; }
.field-clarification-body { max-height: 65vh; overflow-y: auto; color: #243d34; }
fieldset { border: 0; padding: 0; margin: 20px 0; }
legend { font-weight: 600; margin-bottom: 10px; }
.field-candidate { display: flex; gap: 12px; align-items: flex-start; padding: 14px; border: 1px solid #d8e3dd; border-radius: var(--app-radius-lg); margin: 8px 0; cursor: pointer; }
.field-candidate:has(input:checked) { background: #edf9f1; border-color: #168a55; }
input { margin-top: 5px; accent-color: #168a55; } small { display: block; margin-top: 5px; line-height: 1.6; color: #5a6b63; overflow-wrap: anywhere; }
textarea { display: block; box-sizing: border-box; width: 100%; margin-top: 10px; padding: 12px; border: 1px solid #b8c8bf; border-radius: var(--app-radius-md); font: inherit; resize: vertical; }
.field-help { color: #69766f; font-size: var(--app-text-sm); line-height: 1.6; }
.field-actions { position: sticky; bottom: 0; background: white; display: flex; justify-content: flex-end; gap: 12px; padding: 8px 0; }
</style>
